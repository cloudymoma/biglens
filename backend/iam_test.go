package main

import (
	"strings"
	"testing"
)

// JOBS_BY_PROJECT has one row per job, so the Human Users and Service
// Accounts cards must count distinct emails like Total Identities does;
// COUNTIF would count the jobs those identities ran instead.
func TestIAMSummarySQLCountsDistinctIdentities(t *testing.T) {
	sql := strings.Join(strings.Fields(iamSummarySQL("`p`.`region-us`", "7 DAY")), " ")
	for _, want := range []string{
		"COUNT(DISTINCT user_email) AS total_emails",
		"COUNT(DISTINCT IF(ENDS_WITH(user_email, '.gserviceaccount.com'), user_email, NULL)) AS service_accounts",
		"COUNT(DISTINCT IF(NOT ENDS_WITH(user_email, '.gserviceaccount.com'), user_email, NULL)) AS human_users",
		"COUNT(*) AS total_calls",
		"FROM `p`.`region-us`.INFORMATION_SCHEMA.JOBS_BY_PROJECT",
		"WHERE creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY)",
	} {
		if !strings.Contains(sql, want) {
			t.Errorf("iamSummarySQL() missing %q in:\n%s", want, sql)
		}
	}
	if strings.Contains(sql, "COUNTIF") {
		t.Errorf("iamSummarySQL() counts jobs with COUNTIF:\n%s", sql)
	}
}
