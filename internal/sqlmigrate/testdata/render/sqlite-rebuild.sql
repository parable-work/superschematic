-- Migration plan for edge-db (sqlite)
-- from:    a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1
-- to:      b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2
-- plan:    c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3
-- 1 step (1 expand, 0 contract), 2 hazards (1 blocking, 1 copy-table)

-- Expand. Runs before the new servers roll out.

-- Step 1, expand, copyTable table/reading
-- hazard blocking:table/reading
-- hazard copy-table:table/reading
-- foreign keys are off around this step and checked before its commit
CREATE TABLE "reading_new" ("id" TEXT NOT NULL PRIMARY KEY, "value" REAL NOT NULL);
INSERT INTO "reading_new" ("id", "value") SELECT "id", "value" FROM "reading";
DROP TABLE "reading";
ALTER TABLE "reading_new" RENAME TO "reading";
