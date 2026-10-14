# Aurora 生成产物挂到 Agent 回复 —— 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development（本计划已选定该执行方式：每个 Task 一个全新 subagent，任务之间由 Lead 评审并跑验证）。Steps 用 - [ ] 语法跟踪。

**Goal:** 每一轮 Aurora 生成产出的图片/视频成为该轮 Agent 回复评论自己的附件：评论里直接显示可点开预览的图片，侧栏「产物」自动收录，作品库仍独立持有同一份文件。

**Architecture:** 产物提交时（ReportTaskArtifacts 事务内）在 attachment 表写一条「issue 级 + 本 task 标记 + comment_id 为空」的投影行；任务到达终态时把这些未绑定行绑到该轮回复评论上，并在 handler 层广播带附件的 comment:updated。字节只有一份，owner 是 aurora_asset（作品库）：删评论只解除引用，删资产才回收对象并同步清掉引用。前端零改动 —— 评论附件渲染与 Deliverables 侧栏已经存在。

**Tech Stack:** Go（Chi、pgx、sqlc、pgxpool）、PostgreSQL 迁移、现有 events.Bus；测试用 server/internal/testutil 的 dbfx 夹具与 DB 后端测试。

**Spec:** docs/superpowers/specs/2026-10-12-aurora-issue-execution-design.md §1.3（主区域是评论和时间线里已经写下的话，加上已经存上的文件）；本次澄清需求：产物要出现在 Agent 回复里可直接预览，并进入侧栏「产物」。

## Global Constraints

- 不新增外键、不做级联删除；关系在应用代码里校验。
- 每条迁移建的索引必须 CREATE INDEX CONCURRENTLY，且独占一个单语句迁移文件（带 IF NOT EXISTS 与注释）。
- SQL 改动后必须 make sqlc，生成代码与查询一起提交。
- 代码注释英文；提交为 atomic conventional commits。
- Go 测试用 dbfx 夹具与 testutil.Call(t, h, req).Want(status)；产品断言写在测试里，不藏在夹具里。
- 不改 apps/mobile/、不改 13 份技能文档、不改作品库/历史/生成详情页、不改任何已发布 API 响应字段（本计划不新增接口）。
- 定向验证：cd server && go test ./internal/handler/ ./internal/service/ -run 'Aurora' -count=1；最终再跑 make sqlc && go build ./... && pnpm typecheck && pnpm lint && pnpm test。

## File Structure

| 文件 | 责任 | Task |
| --- | --- | --- |
| server/pkg/db/queries/attachment.sql | 绑定/列表/回收查询 | 2,3,5,6 |
| server/pkg/db/queries/comment.sql | 该轮最新 agent 评论查询 | 2 |
| server/pkg/db/queries/aurora.sql | media_url 归属查询 | 5 |
| server/pkg/db/generated/*.go | make sqlc 生成，勿手改 | 2,3,4,5,6 |
| server/internal/handler/aurora_artifact.go | 提交时写投影附件 | 1 |
| server/internal/service/task.go | 终态绑定、兜底评论、输入过滤、失败清理、createAgentComment 返回值 | 2,4,6 |
| server/internal/handler/file.go | publishCommentWithAttachments 与 reclaimAttachmentObjects | 3,5 |
| server/internal/handler/daemon.go | CompleteTask 完成后广播 | 3 |
| server/internal/handler/comment.go、issue.go | 三处对象回收改走 reclaimAttachmentObjects | 5 |
| server/internal/handler/aurora.go | DeleteAuroraAsset 清引用并广播 | 5 |
| server/migrations/588_aurora_asset_media_url_idx.{up,down}.sql | media_url 部分索引 | 5 |
| server/internal/handler/aurora_issue_artifact_test.go（新） | Task 1/3/5 的 handler 测试 | — |
| server/internal/service/aurora_round_attachment_test.go（新） | Task 2/4/6 的 service 测试 | — |

---

### Prerequisite（只读诊断，先做）

用报障那条 issue 对应的 generation 读 GET /api/aurora/generations/{id}，或直接查库 SELECT count(*) FROM aurora_asset WHERE generation_id = $1。

- 有行 → 继续 Task 1。
- 无行 → 产物没落库，是另一个缺陷（候选：LOCAL_UPLOAD_BASE_URL 未配置导致审核拒收；daemon 不在托管沙箱时 attachAuroraArtifacts 在 os.Stat(sandboxRoot) 提前返回）。停下单独报，不在本计划里改提交链路。

---

### Task 1: 产物提交时投影成 issue 附件

**Files:**
- Modify: server/internal/handler/aurora_artifact.go（commitAuroraArtifacts 与其调用点 ReportTaskArtifacts）
- Test: server/internal/handler/aurora_issue_artifact_test.go（新建）

**Interfaces:**
- Consumes: ReportTaskArtifacts 里已有的 task（db.AgentTaskQueue）；normalizedAuroraReportArtifact 的 payload/staging/objectURL；gen.IssueID。
- Produces: func (h *Handler) commitAuroraArtifacts(ctx context.Context, gen db.AuroraGeneration, taskID, agentID pgtype.UUID, artifacts []normalizedAuroraReportArtifact) error；副作用：每个已提交产物多一行 attachment（comment_id 为空）。

**投影 SQL（放进该事务，作为包级常量 insertAuroraProjectionAttachment）:**

~~~sql
INSERT INTO attachment (
    id, workspace_id, issue_id, comment_id, task_id,
    uploader_type, uploader_id, filename, url, content_type, size_bytes
)
SELECT $1, $2, $3, NULL, $4, 'agent', $5, $6, $7, $8, $9
WHERE NOT EXISTS (
    SELECT 1 FROM attachment
    WHERE workspace_id = $2 AND task_id = $4 AND url = $7
)
~~~

参数顺序：attachmentUUID、gen.WorkspaceID、gen.IssueID、staging.TaskID、agentID、payload.Name、objectURL、payload.MIMEType、payload.SizeBytes（uuid 用 uuid.NewV7() 生成）。

- [ ] Step 1: 写失败测试。在 aurora_issue_artifact_test.go 新建两个测试：

~~~go
func TestReportTaskArtifactsProjectsIssueAttachment(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})
	// 复刻 aurora_artifact_test.go:137 那段报告用例的存储装配

	taskID, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	fixture := seedAuroraStaging(t, store, taskID, generationID, "art-1", "xhs-image.png",
		"primary", []byte("png-bytes"), "{}")

	reportAuroraArtifacts(t, taskID, fixture.payload("primary")).Want(http.StatusOK)

	var filename, contentType, uploaderType, taskCol, issueCol string
	var size int64
	var commentID *string
	dbfx.QueryRow(t, attachmentSelectByIssue, issueID).
		Scan(&filename, &contentType, &size, &commentID, &taskCol, &issueCol, &uploaderType)

	if filename != "xhs-image.png" || contentType != "image/png" || size != 9 {
		t.Fatalf("attachment = %s/%s/%d, want xhs-image.png/image/png/9", filename, contentType, size)
	}
	if commentID != nil {
		t.Fatalf("comment_id = %v, want NULL before the round ends", *commentID)
	}
	if taskCol != taskID || issueCol != issueID || uploaderType != "agent" {
		t.Fatalf("binding = task %s issue %s uploader %s", taskCol, issueCol, uploaderType)
	}
}

func TestReportTaskArtifactsProjectionIsIdempotentAndIssueScoped(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	withAuroraModeration(t, &stubModerator{assetDecision: aurora.Decision{Allowed: true}})

	taskID, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	fixture := seedAuroraStaging(t, store, taskID, generationID, "art-1", "xhs-image.png",
		"primary", []byte("png-bytes"), "{}")
	reportAuroraArtifacts(t, taskID, fixture.payload("primary")).Want(http.StatusOK)
	reportAuroraArtifacts(t, taskID, fixture.payload("primary")).Want(http.StatusOK)

	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE issue_id = $1", issueID); n != 1 {
		t.Fatalf("attachment rows = %d, want 1 after a replayed report", n)
	}

	plainTask, plainGeneration := auroraReportFixture(t, "xhs-image")
	plain := seedAuroraStaging(t, store, plainTask, plainGeneration, "art-2", "plain.png",
		"primary", []byte("png-bytes"), "{}")
	reportAuroraArtifacts(t, plainTask, plain.payload("primary")).Want(http.StatusOK)
	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE task_id = $1", plainTask); n != 0 {
		t.Fatalf("issue-less generation wrote %d attachments, want 0", n)
	}
}
~~~

两个夹具常量放在测试文件顶部：

~~~go
const attachmentSelectByIssue = "SELECT filename, content_type, size_bytes, comment_id::text, task_id::text, issue_id::text, uploader_type FROM attachment WHERE issue_id = $1"

// auroraIssueReportFixture 是 issue 轮形状的 task/generation：generation 通过
// issue_id 绑到 issue 上。
func auroraIssueReportFixture(t *testing.T, skillID string) (taskID, generationID, issueID string) {
	t.Helper()
	issueID = dbfx.Issue(t, "aurora issue artifact projection")
	agentID := dbfx.Agent(t, "AuroraIssueArtifactAgent "+uuid.NewString(), handlerTestRuntimeID(t))
	taskID = dbfx.Task(t, agentID, testutil.Cols{
		"runtime_id": handlerTestRuntimeID(t),
		"status":     "running",
		"issue_id":   issueID,
	})
	generationID = dbfx.Insert(t, "aurora_generation", testutil.Cols{
		"workspace_id":     testWorkspaceID,
		"user_id":          testUserID,
		"skill_id":         skillID,
		"prompt":           "issue artifact projection",
		"status":           "running",
		"task_id":          taskID,
		"issue_id":         issueID,
		"credits_reserved": int64(62000000),
	})
	return taskID, generationID, issueID
}
~~~

- [ ] Step 2: 运行确认失败。Run: cd server && go test ./internal/handler/ -run 'TestReportTaskArtifactsProject' -count=1。Expected: FAIL（attachment rows = 0, want 1）。

- [ ] Step 3: 最小实现。ReportTaskArtifacts 里改为 h.commitAuroraArtifacts(r.Context(), gen, task.ID, task.AgentID, bound)；commitAuroraArtifacts 增加 taskID、agentID 两个参数（函数头注释说明这是给投影用的 task/agent 身份），并在循环内既有 UPDATE aurora_artifact_staging 之后、gen.IssueID.Valid 为真时执行 tx.Exec(ctx, insertAuroraProjectionAttachment, ...)。

- [ ] Step 4: 运行确认通过。Run: cd server && go test ./internal/handler/ -run 'TestReportTaskArtifacts' -count=1。Expected: PASS。

- [ ] Step 5: 提交。git add server/internal/handler/aurora_artifact.go server/internal/handler/aurora_issue_artifact_test.go && git commit -m "feat(aurora): project each committed artifact as an issue attachment"

---

### Task 2: 任务结束时把投影挂到该轮回复评论

**Files:**
- Modify: server/pkg/db/queries/attachment.sql、comment.sql、（make sqlc）generated
- Modify: server/internal/service/task.go（createAgentComment 返回值；CompleteTaskWithTransition 的 issue 分支）
- Test: server/internal/service/aurora_round_attachment_test.go（新建）

**Interfaces:**
- Produces: BindIssueAttachmentsToComment(ctx, {CommentID, WorkspaceID, IssueID, TaskID}) ([]db.Attachment, error)；ListUnboundAttachmentsForTask(ctx, {WorkspaceID, IssueID, TaskID}) ([]db.Attachment, error)；GetLatestAgentCommentSince(ctx, {IssueID, WorkspaceID, AuthorID, Since}) (db.Comment, error)；func (s *TaskService) createAgentComment(...) (db.Comment, bool)；func (s *TaskService) bindAuroraRoundArtifacts(ctx context.Context, task db.AgentTaskQueue)。

- [ ] Step 1: 写 SQL，Run: make sqlc，Expected exit 0。

attachment.sql 追加：

~~~sql
-- name: BindIssueAttachmentsToComment :many
-- Bind a run's task-tagged issue attachments to the reply comment it produced.
-- Only rows still unclaimed are eligible, so a replayed completion is a no-op
-- and another run's projections are never stolen.
UPDATE attachment
SET comment_id = @comment_id
WHERE workspace_id = @workspace_id
  AND issue_id = @issue_id
  AND task_id = @task_id
  AND comment_id IS NULL
  AND source_context_id IS NULL
RETURNING *;

-- name: ListUnboundAttachmentsForTask :many
SELECT * FROM attachment
WHERE workspace_id = @workspace_id
  AND issue_id = @issue_id
  AND task_id = @task_id
  AND comment_id IS NULL
  AND source_context_id IS NULL
ORDER BY created_at ASC;
~~~

comment.sql 追加（放在 HasAgentCommentedSince 旁）：

~~~sql
-- name: GetLatestAgentCommentSince :one
-- The reply a run wrote on its issue when the agent posted it itself. Same
-- predicate as HasAgentCommentedSince, newest first: the completion path binds
-- the run's files to exactly the comment that invariant counts.
SELECT * FROM comment
WHERE issue_id = @issue_id
  AND workspace_id = @workspace_id
  AND author_type = 'agent'
  AND author_id = @author_id
  AND created_at >= @since
  AND deleted_at IS NULL
ORDER BY created_at DESC, id DESC
LIMIT 1;
~~~

- [ ] Step 2: 写失败测试（service）。文件顶部复用一个 seed 帮助函数：

~~~go
// seedIssueWithRound 建 issue + running 任务 + 一条本轮产物投影（comment_id 为空）。
func seedIssueWithRound(t *testing.T, svc *TaskService, pool *pgxpool.Pool, workspaceID, userID, agentID string) (issueID, taskID pgtype.UUID) {
	t.Helper()
	ctx := context.Background()
	if err := pool.QueryRow(ctx, issueInsertSQL, workspaceID, userID).Scan(&issueID); err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM issue WHERE id = $1", issueID) })

	var runtimeID string
	pool.QueryRow(ctx, "SELECT runtime_id::text FROM agent WHERE id = $1", agentID).Scan(&runtimeID)
	if err := pool.QueryRow(ctx, taskInsertSQL, agentID, runtimeID, issueID).Scan(&taskID); err != nil {
		t.Fatalf("seed task: %v", err)
	}
	t.Cleanup(func() { pool.Exec(context.Background(), "DELETE FROM agent_task_queue WHERE id = $1", taskID) })

	if _, err := svc.Queries.CreateAttachment(ctx, db.CreateAttachmentParams{
		ID: dbid.NewV7(), WorkspaceID: parseUUID(workspaceID), IssueID: issueID, TaskID: taskID,
		UploaderType: "agent", UploaderID: parseUUID(agentID),
		Filename: "xhs-image.png", Url: "https://cdn.example.test/xhs-image.png",
		ContentType: "image/png", SizeBytes: 9,
	}); err != nil {
		t.Fatalf("seed projection: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), "DELETE FROM attachment WHERE issue_id = $1", issueID)
		pool.Exec(context.Background(), "DELETE FROM comment WHERE issue_id = $1", issueID)
	})
	return issueID, taskID
}

func TestBindAuroraRoundArtifactsBindsToTheRoundsReply(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	issueID, taskID := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)

	reply, err := svc.Queries.CreateComment(context.Background(), db.CreateCommentParams{
		ID: dbid.NewV7(), IssueID: issueID, WorkspaceID: parseUUID(workspaceID),
		AuthorType: "agent", AuthorID: parseUUID(agentID),
		Content: "Produced xhs-image.png", Type: "comment", SourceTaskID: taskID,
	})
	if err != nil {
		t.Fatalf("seed reply: %v", err)
	}
	task, err := svc.Queries.GetAgentTask(context.Background(), taskID)
	if err != nil {
		t.Fatalf("load task: %v", err)
	}

	svc.bindAuroraRoundArtifacts(context.Background(), task)

	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE task_id = $1 AND comment_id = $2", taskID, reply.Comment().ID); n != 1 {
		t.Fatalf("bound attachments = %d, want 1", n)
	}
}

func TestBindAuroraRoundArtifactsNamesFilesWhenNoReplyExists(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	_, taskID := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)

	task, _ := svc.Queries.GetAgentTask(context.Background(), taskID)
	svc.bindAuroraRoundArtifacts(context.Background(), task)

	var content string
	if err := pool.QueryRow(context.Background(),
		"SELECT c.content FROM comment c JOIN attachment a ON a.comment_id = c.id WHERE a.task_id = $1",
		taskID).Scan(&content); err != nil {
		t.Fatalf("no reply comment carrying the file: %v", err)
	}
	if content != "xhs-image.png" {
		t.Fatalf("comment content = %q, want the produced file name", content)
	}
}

func TestBindAuroraRoundArtifactsLeavesOtherRoundsAlone(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	issueID, taskID := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)
	otherTaskID := seedProjectionOnIssue(t, svc, pool, workspaceID, agentID, issueID)

	task, _ := svc.Queries.GetAgentTask(context.Background(), taskID)
	svc.bindAuroraRoundArtifacts(context.Background(), task)

	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE task_id = $1 AND comment_id IS NULL", otherTaskID); n != 1 {
		t.Fatalf("another round's unbound rows = %d, want 1", n)
	}
}
~~~

同文件顶部放三条 SQL 常量与其内容（不要留空）：

~~~go
const issueInsertSQL = "INSERT INTO issue (workspace_id, title, status, priority, creator_type, creator_id) VALUES ($1, 'aurora round reply', 'todo', 'none', 'member', $2) RETURNING id"
const taskInsertSQL = "INSERT INTO agent_task_queue (agent_id, runtime_id, status, priority, issue_id, started_at) VALUES ($1, $2, 'running', 0, $3, now() - interval '1 minute') RETURNING id"
~~~

seedProjectionOnIssue 与 seedIssueWithRound 同体，只是复用传入的 issueID、不再注册 issue 清理，并返回新 taskID。

- [ ] Step 3: 运行确认失败。Run: cd server && go test ./internal/service/ -run 'TestBindAuroraRoundArtifacts' -count=1。Expected: FAIL（bindAuroraRoundArtifacts undefined）。

- [ ] Step 4: 最小实现（server/internal/service/task.go）。

createAgentComment 签名与返回：

~~~go
// createAgentComment writes one agent comment and returns it (false when there
// is nothing to write). Callers that link files to the reply need the row.
func (s *TaskService) createAgentComment(ctx context.Context, issueID, agentID pgtype.UUID, content, commentType string, parentID, sourceTaskID pgtype.UUID) (db.Comment, bool) {
	if content == "" {
		return db.Comment{}, false
	}
	// ...既有实现不变...
	return comment, true
}
~~~

两处调用点：成功合成回复（task.go:4925）改用返回值或忽略；失败系统评论（task.go:5583）忽略。

CompleteTaskWithTransition 的 if task.IssueID.Valid 分支末尾追加：

~~~go
		// A round's produced files belong on the reply that announced them.
		s.bindAuroraRoundArtifacts(ctx, task)
~~~

新方法与注释：

~~~go
// bindAuroraRoundArtifacts attaches the files this round produced to the reply
// it wrote them in. The artifact report already wrote one issue-scoped,
// task-tagged attachment per file; the reply comment is what the person reads,
// and the sidebar's deliverables list is built from comment attachments, so
// this is what makes the files visible in both places.
//
// Best-effort and post-commit, like settleAuroraOnCompleted: the files are
// committed and the generation settled, so a failed bind is logged rather than
// failing the completion. The bind is idempotent: only unclaimed rows move.
func (s *TaskService) bindAuroraRoundArtifacts(ctx context.Context, task db.AgentTaskQueue) {
	if !task.IssueID.Valid {
		return
	}
	pending, err := s.Queries.ListUnboundAttachmentsForTask(ctx, db.ListUnboundAttachmentsForTaskParams{
		WorkspaceID: task.WorkspaceID, IssueID: task.IssueID, TaskID: task.ID,
	})
	if err != nil {
		slog.Warn("aurora round attachment lookup failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	if len(pending) == 0 {
		return
	}
	comment, err := s.latestRoundReplyComment(ctx, task)
	if errors.Is(err, pgx.ErrNoRows) {
		// The run produced files but wrote no reply. Name them in a comment so
		// the output stays reachable: the file names are the same facts the
		// manifest carries, with no invented prose.
		names := make([]string, 0, len(pending))
		for _, attachment := range pending {
			names = append(names, attachment.Filename)
		}
		if _, ok := s.createAgentComment(ctx, task.IssueID, task.AgentID, strings.Join(names, "\\n"), "comment", task.TriggerCommentID, task.ID); !ok {
			slog.Warn("aurora round reply synthesis failed", "task_id", util.UUIDToString(task.ID))
			return
		}
		comment, err = s.latestRoundReplyComment(ctx, task)
	}
	if err != nil {
		slog.Warn("aurora round reply lookup failed", "task_id", util.UUIDToString(task.ID), "error", err)
		return
	}
	if _, err := s.Queries.BindIssueAttachmentsToComment(ctx, db.BindIssueAttachmentsToCommentParams{
		CommentID: comment.ID, WorkspaceID: task.WorkspaceID, IssueID: task.IssueID, TaskID: task.ID,
	}); err != nil {
		slog.Warn("aurora round attachment bind failed", "task_id", util.UUIDToString(task.ID), "error", err)
	}
}

// latestRoundReplyComment is the newest comment this run's agent wrote on the
// issue since the run started.
func (s *TaskService) latestRoundReplyComment(ctx context.Context, task db.AgentTaskQueue) (db.Comment, error) {
	return s.Queries.GetLatestAgentCommentSince(ctx, db.GetLatestAgentCommentSinceParams{
		IssueID: task.IssueID, WorkspaceID: task.WorkspaceID,
		AuthorID: task.AgentID, Since: task.StartedAt,
	})
}
~~~

- [ ] Step 5: 运行确认通过。Run: cd server && go test ./internal/service/ -run 'TestBindAuroraRoundArtifacts' -count=1 && go test ./internal/handler/ -count=1。Expected: PASS（已知既有红集按 AGENTS.md leftover (9) 如实记录）。

- [ ] Step 6: 提交。git add server/pkg/db/queries server/pkg/db/generated server/internal/service/task.go server/internal/service/aurora_round_attachment_test.go && git commit -m "feat(aurora): bind a round's produced files to its reply comment"

---

### Task 3: 完成后广播带附件的评论快照

**Files:**
- Modify: server/pkg/db/queries/attachment.sql、（make sqlc）、server/internal/handler/file.go、server/internal/handler/daemon.go
- Test: server/internal/handler/aurora_issue_artifact_test.go（追加）

**Interfaces:**
- Consumes: h.publish(eventType, workspaceID, actorType, actorID string, payload any)；h.attachmentToResponse(a db.Attachment, mode attachmentURLMode) AttachmentResponse；commentToResponse(c, reactions, attachments)；protocol.EventCommentUpdated；attachmentURLModeSigned。
- Produces: func (h *Handler) publishCommentWithAttachments(ctx context.Context, workspaceID, commentID pgtype.UUID, actorType, actorID string)；GetCommentBoundToTaskAttachments :one。

- [ ] Step 1: 写 SQL 并 make sqlc。

~~~sql
-- name: GetCommentBoundToTaskAttachments :one
-- The comment a task's produced files were linked to, when there is one. Used
-- by the completion handler to rebroadcast that comment with its files.
SELECT comment_id FROM attachment
WHERE task_id = @task_id
  AND comment_id IS NOT NULL
  AND workspace_id = @workspace_id
LIMIT 1;
~~~

- [ ] Step 2: 写失败测试（aurora_issue_artifact_test.go 追加）。

~~~go
func TestPublishCommentWithAttachmentsCarriesTheFiles(t *testing.T) {
	updates := make(chan CommentResponse, 4)
	testHandler.Bus.Subscribe(protocol.EventCommentUpdated, func(e events.Event) {
		payload, ok := e.Payload.(map[string]any)
		if !ok {
			return
		}
		if comment, ok := payload["comment"].(CommentResponse); ok {
			updates <- comment
		}
	})

	_, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	assetURL := "https://cdn.example.test/aurora/xhs-image.png"
	insertAsset(t, generationID, testutil.Cols{"media_url": assetURL})
	commentID := dbfx.Insert(t, "comment", testutil.Cols{
		"issue_id": issueID, "workspace_id": testWorkspaceID,
		"author_type": "agent", "author_id": testAgentID,
		"content": "Produced it", "type": "comment",
	})
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issueID, "comment_id": commentID,
		"uploader_type": "agent", "uploader_id": testAgentID,
		"filename": "xhs-image.png", "url": assetURL,
		"content_type": "image/png", "size_bytes": int64(9),
	})

	testHandler.publishCommentWithAttachments(context.Background(),
		parseUUID(testWorkspaceID), parseUUID(commentID), "system", "")

	select {
	case comment := <-updates:
		if len(comment.Attachments) != 1 || comment.Attachments[0].Filename != "xhs-image.png" {
			t.Fatalf("attachments = %+v", comment.Attachments)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no comment:updated broadcast carrying the round's files")
	}
}
~~~

若 testAgentID 不存在，用 dbfx.Agent(t, ...) 建一个并在测试内复用其 id。

- [ ] Step 3: 运行确认失败。Run: cd server && go test ./internal/handler/ -run 'TestPublishCommentWithAttachments' -count=1。Expected: FAIL（publishCommentWithAttachments undefined）。

- [ ] Step 4: 最小实现。

file.go 新增（与 groupAttachments 并列）：

~~~go
// publishCommentWithAttachments rebroadcasts one comment with its current
// attachments. A file can be linked to a comment after that comment was
// broadcast — the artifact report writes the row, the completion binds it —
// and clients replace a cached comment with the snapshot they receive, so the
// comment has to be re-sent carrying the files it now has.
//
// Unlike groupAttachments this takes the workspace explicitly: the completion
// path has no member request of its own to resolve one from.
func (h *Handler) publishCommentWithAttachments(ctx context.Context, workspaceID, commentID pgtype.UUID, actorType, actorID string) {
	comment, err := h.Queries.GetCommentInWorkspace(ctx, db.GetCommentInWorkspaceParams{
		ID: commentID, WorkspaceID: workspaceID,
	})
	if err != nil {
		return
	}
	attachments, err := h.Queries.ListAttachmentsByComment(ctx, db.ListAttachmentsByCommentParams{
		CommentID: commentID, WorkspaceID: workspaceID,
	})
	if err != nil {
		slog.Warn("publish comment attachments: load failed", "comment_id", uuidToString(commentID), "error", err)
		return
	}
	responses := make([]AttachmentResponse, 0, len(attachments))
	for _, attachment := range attachments {
		responses = append(responses, h.attachmentToResponse(attachment, attachmentURLModeSigned))
	}
	h.publish(protocol.EventCommentUpdated, uuidToString(workspaceID), actorType, actorID, map[string]any{
		"comment": commentToResponse(comment, nil, responses),
	})
}
~~~

daemon.go 的 Handler.CompleteTask 在 CompleteTaskWithTransition 成功之后追加：

~~~go
	// A completed Aurora round may have just linked its produced files to the
	// reply comment. Rebroadcast that comment so clients that already rendered
	// it pick the files up without waiting for a refetch.
	if bound, err := h.Queries.GetCommentBoundToTaskAttachments(r.Context(), db.GetCommentBoundToTaskAttachmentsParams{
		TaskID: task.ID, WorkspaceID: task.WorkspaceID,
	}); err == nil {
		h.publishCommentWithAttachments(r.Context(), task.WorkspaceID, bound, "agent", uuidToString(task.AgentID))
	} else if !errors.Is(err, pgx.ErrNoRows) {
		slog.Warn("completion: bound attachment lookup failed", "task_id", taskID, "error", err)
	}
~~~

- [ ] Step 5: 运行确认通过。Run: cd server && go test ./internal/handler/ -run 'TestPublishCommentWithAttachments|TestCompleteTask' -count=1。Expected: PASS。

- [ ] Step 6: 提交。git add server/pkg/db/queries server/pkg/db/generated server/internal/handler && git commit -m "feat(aurora): rebroadcast the reply carrying the round's files"

---

### Task 4: 本轮产物不再回喂成下一轮输入

**Files:**
- Modify: server/pkg/db/queries/attachment.sql、（make sqlc）、server/internal/service/task.go（auroraRoundAttachmentIDs）
- Test: server/internal/service/aurora_round_attachment_test.go（追加）

**Interfaces:**
- Produces: ListAuroraRoundInputAttachments(ctx, {IssueID, WorkspaceID}) ([]db.Attachment, error)。

- [ ] Step 1: 写 SQL 并 make sqlc。

~~~sql
-- name: ListAuroraRoundInputAttachments :many
-- The files a person gave an Aurora issue. A projection written by an earlier
-- round carries a task_id and is deliberately excluded: a round's own output
-- is not its next round's input.
SELECT * FROM attachment
WHERE issue_id = $1
  AND workspace_id = $2
  AND task_id IS NULL
ORDER BY created_at ASC;
~~~

- [ ] Step 2: 写失败测试。

~~~go
func TestAuroraRoundInputsExcludeProducedFiles(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	issueID, _ := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)

	human, err := svc.Queries.CreateAttachment(context.Background(), db.CreateAttachmentParams{
		ID: dbid.NewV7(), WorkspaceID: parseUUID(workspaceID), IssueID: issueID,
		UploaderType: "member", UploaderID: parseUUID(userID),
		Filename: "brief.png", Url: "https://cdn.example.test/brief.png",
		ContentType: "image/png", SizeBytes: 5,
	})
	if err != nil {
		t.Fatalf("seed human attachment: %v", err)
	}
	issue, err := svc.Queries.GetIssue(context.Background(), issueID)
	if err != nil {
		t.Fatalf("load issue: %v", err)
	}

	ids, err := svc.auroraRoundAttachmentIDs(context.Background(), issue)
	if err != nil {
		t.Fatalf("auroraRoundAttachmentIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != util.UUIDToString(human.ID) {
		t.Fatalf("round inputs = %v, want only the human-supplied %s", ids, util.UUIDToString(human.ID))
	}
}
~~~

- [ ] Step 3: 运行确认失败。Run: cd server && go test ./internal/service/ -run 'TestAuroraRoundInputsExclude' -count=1。Expected: FAIL（得到 2 个 id）。

- [ ] Step 4: 最小实现。auroraRoundAttachmentIDs 改调 ListAuroraRoundInputAttachments，并把注释改写为：inputs are the issue's human-supplied files; a run's own projections carry a task_id and are excluded, because feeding this round's output back as the next round's input would make the model re-edit its own deliverable。

- [ ] Step 5: 运行确认通过。Run: cd server && go test ./internal/service/ -run 'Aurora' -count=1。Expected: PASS。

- [ ] Step 6: 提交。git add server/pkg/db/queries server/pkg/db/generated server/internal/service/task.go server/internal/service/aurora_round_attachment_test.go && git commit -m "fix(aurora): keep a round's produced files out of the next round's inputs"

---

### Task 5: 所有权 —— 删回复不删库、删库清引用

**Files:**
- Modify: server/pkg/db/queries/attachment.sql、aurora.sql、（make sqlc）
- Create: server/migrations/588_aurora_asset_media_url_idx.up.sql、.down.sql
- Modify: server/internal/handler/file.go、comment.go、issue.go、aurora.go
- Test: server/internal/handler/aurora_issue_artifact_test.go（追加）

**Interfaces:**
- Produces: ListAuroraAssetMediaURLs(ctx, []string) ([]string, error)；DeleteAttachmentsByURL(ctx, {Url, WorkspaceID}) ([]db.DeleteAttachmentsByURLRow, error)；func (h *Handler) reclaimAttachmentObjects(ctx context.Context, urls []string)。

- [ ] Step 1: 写 SQL 与迁移，make sqlc。

aurora.sql：

~~~sql
-- name: ListAuroraAssetMediaURLs :many
-- Which of these stored objects the content library still points at. Used when
-- a comment or issue delete releases attachment objects: a completed Aurora
-- run stores its deliverable once and the library owns those bytes, so the
-- reply's link going away must not reclaim the file.
SELECT media_url FROM aurora_asset
WHERE media_url = ANY(@urls::text[])
  AND media_url IS NOT NULL;
~~~

attachment.sql：

~~~sql
-- name: DeleteAttachmentsByURL :many
-- Drop every attachment row pointing at one stored object. The library calls
-- this when it deletes an asset: the bytes are going away, so a reply that
-- showed them must stop claiming to have a file.
DELETE FROM attachment
WHERE url = @url AND workspace_id = @workspace_id
RETURNING comment_id;
~~~

server/migrations/588_aurora_asset_media_url_idx.up.sql：

~~~sql
-- The library-ownership check (ListAuroraAssetMediaURLs) runs on every comment
-- or issue delete, so the media_url lookup gets its own index. Own file because
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS aurora_asset_media_url_idx
    ON aurora_asset (media_url)
    WHERE media_url IS NOT NULL;
~~~

server/migrations/588_aurora_asset_media_url_idx.down.sql：

~~~sql
DROP INDEX CONCURRENTLY IF EXISTS aurora_asset_media_url_idx;
~~~

- [ ] Step 2: 写失败测试。

~~~go
func TestReclaimAttachmentObjectsKeepsLibraryOwnedBytes(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	_, generationID, _ := auroraIssueReportFixture(t, "xhs-image")

	assetURL := "https://cdn.example.test/aurora/xhs-image.png"
	insertAsset(t, generationID, testutil.Cols{"media_url": assetURL})

	testHandler.reclaimAttachmentObjects(context.Background(), []string{assetURL})
	if len(store.deleted) != 0 {
		t.Fatalf("deleted keys = %v, want none: the library still owns this object", store.deleted)
	}

	orphan := "https://cdn.example.test/upload/plain.png"
	testHandler.reclaimAttachmentObjects(context.Background(), []string{orphan})
	if len(store.deleted) != 1 || store.deleted[0] == assetURL {
		t.Fatalf("deleted keys = %v, want exactly the orphan", store.deleted)
	}
}

func TestDeleteAuroraAssetUnlinksTheReplyAndRebroadcastsIt(t *testing.T) {
	store := &artifactTestStorage{}
	withArtifactStorage(t, store)
	_, generationID, issueID := auroraIssueReportFixture(t, "xhs-image")
	assetURL := "https://cdn.example.test/aurora/xhs-image.png"
	assetID := insertAsset(t, generationID, testutil.Cols{"media_url": assetURL})
	commentID := dbfx.Insert(t, "comment", testutil.Cols{
		"issue_id": issueID, "workspace_id": testWorkspaceID,
		"author_type": "agent", "author_id": testAgentID,
		"content": "Produced it", "type": "comment", "revision": int64(3),
	})
	dbfx.Insert(t, "attachment", testutil.Cols{
		"workspace_id": testWorkspaceID, "issue_id": issueID, "comment_id": commentID,
		"uploader_type": "agent", "uploader_id": testAgentID,
		"filename": "xhs-image.png", "url": assetURL,
		"content_type": "image/png", "size_bytes": int64(9),
	})

	updates := make(chan struct{}, 4)
	testHandler.Bus.Subscribe(protocol.EventCommentUpdated, func(events.Event) { updates <- struct{}{} })

	req := withURLParam(newRequest(http.MethodDelete, "/api/aurora/assets/"+assetID, nil), "id", assetID)
	testutil.Call(t, testHandler.DeleteAuroraAsset, req).Want(http.StatusNoContent)

	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE url = $1", assetURL); n != 0 {
		t.Fatalf("attachment rows for the deleted asset = %d, want 0", n)
	}
	var revision int64
	dbfx.QueryRow(t, "SELECT revision FROM comment WHERE id = $1", commentID).Scan(&revision)
	if revision <= 3 {
		t.Fatalf("comment revision = %d, want bumped past 3", revision)
	}
	select {
	case <-updates:
	case <-time.After(2 * time.Second):
		t.Fatal("deleting a library asset did not rebroadcast the reply")
	}
}
~~~

- [ ] Step 3: 运行确认失败。Run: cd server && go test ./internal/handler/ -run 'TestReclaimAttachmentObjectsKeeps|TestDeleteAuroraAssetUnlinks' -count=1。Expected: FAIL。

- [ ] Step 4: 最小实现。

file.go 新增：

~~~go
// reclaimAttachmentObjects deletes the stored objects a deleted comment or
// issue released, minus the ones the Aurora library still points at.
//
// Ownership: a completed Aurora run stores its deliverable once, and the
// library (aurora_asset) owns those bytes; the reply comment is a view of the
// same object. Deleting the reply therefore unlinks the file without
// destroying the library entry, which is what keeps the asset row from
// dangling. A failed ownership read skips the reclaim entirely: leaking an
// object is recoverable, silently deleting a library deliverable is not.
func (h *Handler) reclaimAttachmentObjects(ctx context.Context, urls []string) {
	if h.Storage == nil || len(urls) == 0 {
		return
	}
	libraryOwned, err := h.Queries.ListAuroraAssetMediaURLs(ctx, urls)
	if err != nil {
		slog.Error("reclaim attachments: ownership lookup failed", "error", err)
		return
	}
	owned := make(map[string]struct{}, len(libraryOwned))
	for _, url := range libraryOwned {
		owned[url] = struct{}{}
	}
	pending := make([]string, 0, len(urls))
	for _, url := range urls {
		if _, ok := owned[url]; ok {
			continue
		}
		pending = append(pending, url)
	}
	h.deleteS3Objects(ctx, pending)
}
~~~

三处调用点把 h.deleteS3Objects(...) 换成 h.reclaimAttachmentObjects(...)：comment.go:3741、issue.go:4324、issue.go:4898。

aurora.go 的 DeleteAuroraAsset 在删掉对象与行之后追加：

~~~go
	// The reply that showed this file must stop claiming to have it: drop the
	// reference rows, bump each affected comment's revision and rebroadcast it,
	// so no client keeps a broken thumbnail.
	removed, err := h.Queries.DeleteAttachmentsByURL(r.Context(), db.DeleteAttachmentsByURLParams{
		Url: mediaURL, WorkspaceID: asset.WorkspaceID,
	})
	if err != nil {
		slog.Error("failed to unlink deleted asset from comments", "asset_id", uuidToString(asset.ID), "error", err)
	} else {
		seen := make(map[string]struct{}, len(removed))
		for _, row := range removed {
			commentID := uuidToString(row.CommentID)
			if _, ok := seen[commentID]; ok {
				continue
			}
			seen[commentID] = struct{}{}
			if _, err := h.Queries.BumpCommentRevision(r.Context(), db.BumpCommentRevisionParams{
				ID: row.CommentID, WorkspaceID: asset.WorkspaceID,
			}); err != nil && !errors.Is(err, pgx.ErrNoRows) {
				slog.Warn("failed to bump comment revision after asset delete", "comment_id", commentID, "error", err)
			}
			h.publishCommentWithAttachments(r.Context(), asset.WorkspaceID, row.CommentID, "system", "")
		}
	}
~~~

- [ ] Step 5: 运行确认通过。Run: cd server && go test ./internal/handler/ -run 'TestReclaimAttachmentObjectsKeeps|TestDeleteAuroraAssetUnlinks|TestDeleteComment|TestDeleteIssue' -count=1。Expected: PASS。

- [ ] Step 6: 提交。git add server/migrations server/pkg/db/queries server/pkg/db/generated server/internal/handler && git commit -m "fix(aurora): keep library bytes when a reply is deleted, unlink on asset delete"

---

### Task 6: 失败轮次清掉未绑定的投影

**Files:**
- Modify: server/pkg/db/queries/attachment.sql、（make sqlc）、server/internal/service/task.go（失败终态路径）
- Test: server/internal/service/aurora_round_attachment_test.go（追加）

**Interfaces:**
- Produces: DeleteUnboundAttachmentsForTask(ctx, {WorkspaceID, IssueID, TaskID}) (int64, error)；func (s *TaskService) dropUnboundAuroraRoundArtifacts(ctx context.Context, task db.AgentTaskQueue)。

- [ ] Step 1: 写 SQL 并 make sqlc。

~~~sql
-- name: DeleteUnboundAttachmentsForTask :execrows
-- A round that ended without a reply (task failed, or the completion callback
-- never arrived) leaves its projections unbound: invisible, and pointing at an
-- object the library kept. Remove them so a refunded run leaves nothing behind.
DELETE FROM attachment
WHERE workspace_id = @workspace_id
  AND issue_id = @issue_id
  AND task_id = @task_id
  AND comment_id IS NULL;
~~~

- [ ] Step 2: 写失败测试。

~~~go
func TestFailedRoundDropsItsUnboundProjections(t *testing.T) {
	svc, pool, workspaceID, userID, agentID := newAuroraCompletionService(t)
	issueID, taskID := seedIssueWithRound(t, svc, pool, workspaceID, userID, agentID)

	task, _ := svc.Queries.GetAgentTask(context.Background(), taskID)
	svc.dropUnboundAuroraRoundArtifacts(context.Background(), task)
	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE task_id = $1", taskID); n != 0 {
		t.Fatalf("unbound projections left = %d, want 0", n)
	}

	reply, err := svc.Queries.CreateComment(context.Background(), db.CreateCommentParams{
		ID: dbid.NewV7(), IssueID: issueID, WorkspaceID: parseUUID(workspaceID),
		AuthorType: "agent", AuthorID: parseUUID(agentID), Content: "kept", Type: "comment",
	})
	if err != nil {
		t.Fatalf("seed reply: %v", err)
	}
	bound, err := svc.Queries.CreateAttachment(context.Background(), db.CreateAttachmentParams{
		ID: dbid.NewV7(), WorkspaceID: parseUUID(workspaceID), IssueID: issueID,
		CommentID: reply.Comment().ID, TaskID: taskID,
		UploaderType: "agent", UploaderID: parseUUID(agentID),
		Filename: "kept.png", Url: "https://cdn.example.test/kept.png",
		ContentType: "image/png", SizeBytes: 1,
	})
	if err != nil {
		t.Fatalf("seed bound attachment: %v", err)
	}

	svc.dropUnboundAuroraRoundArtifacts(context.Background(), task)
	if n := dbfx.Count(t, "SELECT count(*) FROM attachment WHERE id = $1", bound.ID); n != 1 {
		t.Fatalf("bound attachment was removed, want kept")
	}
}
~~~

- [ ] Step 3: 运行确认失败。Run: cd server && go test ./internal/service/ -run 'TestFailedRoundDrops' -count=1。Expected: FAIL（方法未定义）。

- [ ] Step 4: 最小实现。

~~~go
// dropUnboundAuroraRoundArtifacts clears the projections a round wrote but
// never got to link: the task failed, so the person is not charged for the run
// and the file must not appear as if it had been delivered. Rows bound to a
// reply that was actually written are left alone.
func (s *TaskService) dropUnboundAuroraRoundArtifacts(ctx context.Context, task db.AgentTaskQueue) {
	if !task.IssueID.Valid {
		return
	}
	if _, err := s.Queries.DeleteUnboundAttachmentsForTask(ctx, db.DeleteUnboundAttachmentsForTaskParams{
		WorkspaceID: task.WorkspaceID, IssueID: task.IssueID, TaskID: task.ID,
	}); err != nil {
		slog.Warn("aurora round projection cleanup failed", "task_id", util.UUIDToString(task.ID), "error", err)
	}
}
~~~

在失败终态路径（task.go:5531-5590 一段，settleAuroraOnFailed 与失败系统评论之后）调用 s.dropUnboundAuroraRoundArtifacts(ctx, task)。

- [ ] Step 5: 运行确认通过。Run: cd server && go test ./internal/service/ -run 'Aurora|FailedRound' -count=1。Expected: PASS。

- [ ] Step 6: 提交。git add server/pkg/db/queries server/pkg/db/generated server/internal/service/task.go server/internal/service/aurora_round_attachment_test.go && git commit -m "fix(aurora): drop a failed round's unbound artifact projections"

---

### Task 7: 全量验证与真机验收

**Files:** 无代码改动（发现问题回到对应 Task）。

- [ ] Step 1: make sqlc && cd server && go build ./...。Expected: 无 diff、exit 0。
- [ ] Step 2: cd server && go test ./internal/handler/ ./internal/service/ -run 'Aurora' -count=1；cd server && go test ./internal/daemon/ -count=1。Expected: PASS；已知既有红集按 AGENTS.md leftover (9) 如实记录。
- [ ] Step 3: pnpm typecheck && pnpm lint && pnpm test。Expected: PASS（前端零改动，仅回归）。
- [ ] Step 4: 真机验收（需要已配置 Fleet 的环境）：提交一次图片生成 → 完成后 Agent 回复气泡里直接出现图片，点击可预览、可下载；侧栏产物计数 +1 且可 View all；第二轮同名文件显示 v2；needs_input 轮无附件；删回复不影响作品库下载；删作品库资产后评论无坏图；普通 issue 行为不变。
- [ ] Step 5: 若 Step 4 暴露缺陷，回到对应 Task 修；否则无需提交。

---

## Self-Review

**Spec coverage:** §1.3「看 Agent 已经写下的话和已经存上的文件」→ Task 1/2/3；「没存上文件的轮次不加文件数」不变（Task 2/6 不触碰计费）；「一条生成对一条 issue」→ Task 2 按 task 绑定，多轮各自成对；needs_input 无产物即无附件；删除一致性 → Task 5；本轮产物不回喂 → Task 4。

**Placeholder scan:** 无 TBD/TODO/implement later；每个 SQL、函数签名与测试断言都写出实体；夹具复用来源（aurora_artifact_test.go 的报告装配、newAuroraCompletionService、insertAsset）均指名到现有文件。

**Type consistency:** commitAuroraArtifacts(ctx, gen, taskID, agentID, artifacts) 定义与调用一致；bindAuroraRoundArtifacts(ctx, task) / dropUnboundAuroraRoundArtifacts(ctx, task) 一致；publishCommentWithAttachments(ctx, workspaceID, commentID, actorType, actorID) 一致；查询名 BindIssueAttachmentsToComment / ListUnboundAttachmentsForTask / GetLatestAgentCommentSince / GetCommentBoundToTaskAttachments / ListAuroraRoundInputAttachments / ListAuroraAssetMediaURLs / DeleteAttachmentsByURL / DeleteUnboundAttachmentsForTask 全篇一致。

**两个已定取舍（评审可推翻）：** ① 删回复不删作品库字节（library owns bytes）；改成「删评论连库一起删」只需把 reclaimAttachmentObjects 换成同时删 aurora_asset 行。② 无回复但有产物时用产物文件名建一条评论；改成「只记 warning」则改 Task 2 第二个测试。

## Execution

按用户选择：**Subagent-Driven**（superpowers:subagent-driven-development）。每个 Task 派一个全新 subagent，只给它该 Task 的正文 + Global Constraints + File Structure；它完成后由 Lead 跑该 Task 的验证命令、审查 diff，再放行下一个 Task。Task 之间的写入范围互不重叠（queries/generated 由同一次 make sqlc 提交，跨 Task 顺序执行以避免并发改同一批文件）。


---

## 实施记录（2026-10-14，subagent-driven 执行）

**提交（a1e8e6af3 → HEAD）**

| 提交 | 内容 |
| --- | --- |
| efadc3b3f | Task 1：产物提交时在同一事务内投影成 issue 附件（幂等） |
| 81bc7c4a5 | Task 1 复核修正：复用 fixture、补 url/uploader 断言、绑定后重放幂等 |
| 5d3cfd2f3 | Task 2：结束时绑定到该轮回复评论（三个 sqlc 查询 + 兜底评论） |
| e93eb24b2 | 修正 round-reply 查询注释 |
| e33a7a1be | Task 3：completion 广播带附件的 comment:updated |
| e9f1391f1 | Task 4 |
| 6ebca2ef7 | Task 5：所有权（删回复不删库、删资产清引用）+ 迁移 588 |
| f3bbbfe86 | 测试修复：订阅者不再阻塞包级测试运行 |
| b3f7ac390 | Task 6：失败轮次清掉未绑定投影 |
| 9a02f5557 | /simplify 清理 |

**与计划的偏差**

1. **Task 4 改为在共享查询上收口**：不新增 ListAuroraRoundInputAttachments，而是给 ListAttachmentsByIssue 加 task_id IS NULL。一次改动同时修掉三件事：本轮产物不再回喂下一轮输入、移动端描述灯箱不再把 run 投影当成描述附件、GetIssue 的 issue 级附件列表不再把产物当成人给的附件。六个调用点语义一致，无既有测试因此变红。
2. **sqlc 生成类型**：DeleteAttachmentsByURL 生成 []pgtype.UUID（单列 RETURNING），ListAuroraAssetMediaURLs 生成 []pgtype.Text；调用方按实际类型适配。
3. **db.AgentTaskQueue 没有 WorkspaceID 列**：新增 roundWorkspace(task) 统一经 ResolveTaskWorkspaceID 解析；完成回调路径直接复用 requireDaemonTaskAccessWithWorkspace 已校验的 workspace。
4. **environment**：本 checkout 的 Go 测试必须带 DATABASE_URL（set -a; . ./.env.worktree; set +a），否则会打到过期的 multica 库并出现「failed to load generation」等假失败 —— 即 AGENTS.md leftover (9) 描述的既有红集。

**/simplify 四路复核结论与处置**

- 已改：发布快照重复（helper 内联 reactions 未动，跳过见下）、createAgentComment 返回值被丢弃后又查一次（改为直接用返回值）、两个 round helper 重复的 workspace 解析（抽出 roundWorkspace）、daemon 里重复的 parseUUID（提为局部变量）。
- 已改（效率/altitude）：新增 HasUnboundAttachmentsForTask 探针，普通任务完成/失败不再付 GetIssue + 查询两次往返；round-reply 查询优先用 source_task_id 精确匹配（作者+时间窗保留为兜底，防未打标的回复被漏掉导致重复评论）；DeleteAuroraAsset 的 unlink 块补 mediaURL 空守卫与 NULL comment_id 跳过（省掉每次删除的空转 bump/查询）。
- 跳过并记录：① UploadFile/DeleteAttachment 的两处 comment:updated 发布未改为调用新 helper —— 它们按请求解析 URL 模式并载入 reactions，属本次 diff 之外的既有热路径，改动收益小于回归面；② 投影 INSERT 的列清单仍是手写 SQL（未改调 CreateAttachment，后者的 CTE 会顺带 bump issue revision，语义漂移）；③ GetLatestAgentCommentSince 与 HasAgentCommentedSince 仍共享谓词（一个返回行一个 EXISTS，合并需拆解组合）；④ "库持有字节" 仍以 media_url 相等表达（改为 attachment 记录 asset id 需新列 + 迁移，超出本次范围）；⑤ 测试层 comment/attachment 插桩的重复（安全但可读性小损）。

**验证结果**

- make sqlc 后 git diff 为空；go build ./... 通过。
- go test ./internal/service/ -run 'Aurora|Bind|Fail' -count=1 → ok 14.4s。
- go test ./internal/handler/ -run 'Aurora|Attachment|Comment' -count=1 → 43.2s，唯一失败是 TestAuroraFleetProvisionToClaim（503 fleet unavailable，需真实 Fleet，环境性，与本改动无关）。此前因测试订阅者阻塞导致的整体挂起已修复。
- 真机验收（提交一次生成 → 回复内图片可预览/下载 + 侧栏产物计数）尚未执行：本工作区没有运行中的 Fleet/API，留给有真实环境时按第 1 节验收清单执行。

