-- The library-ownership check (ListAuroraAssetMediaURLs) runs on every comment
-- or issue delete, so the media_url lookup gets its own index. Own file because
-- CREATE INDEX CONCURRENTLY cannot run inside a transaction.
CREATE INDEX CONCURRENTLY IF NOT EXISTS aurora_asset_media_url_idx
    ON aurora_asset (media_url)
    WHERE media_url IS NOT NULL;
