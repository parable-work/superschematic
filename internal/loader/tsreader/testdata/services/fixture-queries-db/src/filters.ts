// The dashboard's filter, which every report of dashboard.schema.ts reads
// its lines through, as li (D95): a range of dates and, each when set, some
// accounts, some categories and a text in the line's name. A query's SQL
// interpolates it as a string constant, so the reports share one
// declaration, and queries.lock hashes each report's SQL with it in place:
// editing it makes every report that reads it a query to verify again.
const IN_RANGE = `li."date" BETWEEN :start AND :end`;

export const LINE_FILTER = `${IN_RANGE}
      AND (:accountIds::uuid[] IS NULL OR li.account_id = ANY(:accountIds))
      AND (:categoryIds::uuid[] IS NULL OR li.category_id = ANY(:categoryIds))
      AND (:search::text IS NULL OR li."name" ILIKE '%' || :search || '%')`;
