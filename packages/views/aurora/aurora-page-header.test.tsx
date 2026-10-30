import { describe, expect, it } from "vitest";
import { render, screen } from "@testing-library/react";
import { Sparkles } from "@multica/ui/icons";
import { AuroraPageHeader, AuroraPageState } from "./aurora-page-header";

describe("AuroraPageHeader", () => {
  it("renders the eyebrow, heading, facts and actions with decorative icon", () => {
    const { container } = render(
      <AuroraPageHeader
        icon={Sparkles}
        eyebrow="AI tools"
        title="Create"
        meta={<span>16 skills</span>}
        actions={<button type="button">Top up</button>}
      />,
    );
    expect(screen.getByRole("heading", { level: 1, name: "Create" })).toBeVisible();
    expect(screen.getByText("AI tools")).toHaveClass("aurora-eyebrow");
    expect(screen.getByText("16 skills").parentElement).toHaveClass("aurora-page-meta");
    expect(screen.getByRole("button", { name: "Top up" }).parentElement).toHaveClass(
      "aurora-page-actions",
    );
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
  });

  it("omits optional slots rather than leaving empty rows", () => {
    const { container } = render(
      <AuroraPageHeader icon={Sparkles} title="Create" />,
    );
    expect(container.querySelector(".aurora-eyebrow")).toBeNull();
    expect(container.querySelector("svg")).toBeNull();
    expect(container.querySelector(".aurora-page-meta")).toBeNull();
    expect(container.querySelector(".aurora-page-actions")).toBeNull();
    expect(screen.getByRole("heading", { level: 1 })).toBeVisible();
  });
});

describe("AuroraPageState", () => {
  it("announces a destructive failure without colouring its title", () => {
    const { container } = render(
      <AuroraPageState
        role="alert"
        tone="destructive"
        icon={Sparkles}
        title="Could not load the skill directory"
      />,
    );
    expect(screen.getByRole("alert")).toHaveClass("aurora-empty");
    expect(screen.getByRole("alert")).toHaveTextContent(
      "Could not load the skill directory",
    );
    expect(container.querySelector("svg")).toHaveClass("text-destructive");
    expect(container.querySelector("svg")).toHaveAttribute("aria-hidden", "true");
    expect(screen.getByRole("heading")).not.toHaveClass("text-destructive");
  });

  it("renders optional description and recovery actions in a warning status", () => {
    const { container } = render(
      <AuroraPageState
        role="status"
        tone="warning"
        icon={Sparkles}
        title="No generations yet"
        description="Finished generations and their results appear here."
        actions={<button type="button">Try again</button>}
      />,
    );
    expect(screen.getByRole("status")).toHaveTextContent("No generations yet");
    expect(
      screen.getByText("Finished generations and their results appear here."),
    ).toBeVisible();
    expect(screen.getByRole("button", { name: "Try again" }).parentElement).toHaveClass(
      "aurora-empty-actions",
    );
    expect(container.querySelector("svg")).toHaveClass("text-warning");
  });

  it("defaults to a muted state without live region or empty optional slots", () => {
    const { container } = render(
      <AuroraPageState icon={Sparkles} title="No matches" />,
    );
    expect(container.firstChild).not.toHaveAttribute("role");
    expect(container.querySelector("svg")).toHaveClass("text-muted-foreground");
    expect(container.querySelector("p")).toBeNull();
    expect(container.querySelector(".aurora-empty-actions")).toBeNull();
  });
});
