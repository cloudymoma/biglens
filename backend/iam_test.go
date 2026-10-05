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

func TestIAMQueriesAndRollups(t *testing.T) {
	callerSQL := callerSummarySQL("`p`.`region-us`", "creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY) AND user_email IS NOT NULL AND IFNULL(statement_type, '') != 'SCRIPT'")
	for _, want := range []string{
		"ENDS_WITH(user_email, '.gserviceaccount.com') AS is_sa",
		"IFNULL(AVG(TIMESTAMP_DIFF(end_time, start_time, MILLISECOND)) / 1000.0, 0.0) AS avg_duration_sec",
		"IFNULL(statement_type, '') != 'SCRIPT'",
	} {
		if !strings.Contains(callerSQL, want) {
			t.Errorf("callerSummarySQL missing %q in:\n%s", want, callerSQL)
		}
	}

	s, top := rollupIdentityStats([]callerStatRow{
		{Email: "bot@p.iam.gserviceaccount.com", IsSA: true, TotalCalls: 100, TotalSlotMs: 5000, AvgDuration: 0.25},
		{Email: "alice@example.com", IsSA: false, TotalCalls: 40, TotalSlotMs: 2000, AvgDuration: 1.5},
	}, 1)
	if s.TotalEmails != 2 || s.ServiceAccounts != 1 || s.HumanUsers != 1 || s.TotalCalls != 140 {
		t.Errorf("unexpected IAMSummary: %+v", s)
	}
	if len(top) != 1 || top[0].Email != "bot@p.iam.gserviceaccount.com" || top[0].AvgDuration != 0.25 {
		t.Errorf("unexpected TopCallers: %+v", top)
	}

	longSQL := longWindowIAMSQL("`p`.`region-us`")
	if !strings.Contains(longSQL, "INTERVAL 180 DAY") || !strings.Contains(longSQL, "IFNULL(statement_type, '') != 'SCRIPT'") {
		t.Errorf("unexpected longWindowIAMSQL:\n%s", longSQL)
	}
	inactive, newActors := splitLongWindowIAM([]longWindowIAMRow{
		{Email: "idle@example.com", DaysIdle: 45, TotalCalls: 12, LastActive: "2026-08-01T00:00:00Z"},
		{Email: "fresh@example.com", DaysIdle: 1, FirstSeen90d: "2026-10-03T00:00:00Z", Jobs90d: 5, IsNewActor: true},
	}, 7, []string{"IDLE@example.com", "fresh@example.com"})
	if len(inactive) != 1 || inactive[0].Email != "idle@example.com" {
		t.Errorf("unexpected inactive list: %+v", inactive)
	}
	if len(newActors) != 1 || newActors[0].Email != "fresh@example.com" {
		t.Errorf("unexpected newActors list: %+v", newActors)
	}

	ohSQL := offHoursSQL("`p`.`region-us`", "creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY)")
	for _, want := range []string{
		"GROUP BY GROUPING SETS ((dow, hr), (email))",
		"COUNTIF(hr NOT BETWEEN 8 AND 19 OR dow IN (1, 7)) AS off_hours_jobs",
	} {
		if !strings.Contains(ohSQL, want) {
			t.Errorf("offHoursSQL missing %q in:\n%s", want, ohSQL)
		}
	}
	cells, ohTop := splitOffHoursRows([]offHoursGroupingRow{
		{Dow: 1, Hr: 14, IsCell: 1, Jobs: 9},
		{Email: "weekend@example.com", IsCell: 0, Jobs: 12, OffHoursJobs: 9},
		{Email: "daytime@example.com", IsCell: 0, Jobs: 20, OffHoursJobs: 0},
	})
	if len(cells) != 1 || cells[0].Jobs != 9 {
		t.Errorf("unexpected off-hours cells: %+v", cells)
	}
	if len(ohTop) != 1 || ohTop[0].Email != "weekend@example.com" || ohTop[0].Jobs != 9 {
		t.Errorf("unexpected off-hours top: %+v", ohTop)
	}

	if got := escapeLikePattern(`sa_prod%1\test`); got != `sa\_prod\%1\\test` {
		t.Errorf("escapeLikePattern = %q", got)
	}
	filtered := filterEmailsByPrefix([]string{"alice@example.com", "sa_alice@p.iam.gserviceaccount.com", "bob@example.com"}, "alice", 20)
	if len(filtered) != 2 || filtered[0] != "alice@example.com" || filtered[1] != "sa_alice@p.iam.gserviceaccount.com" {
		t.Errorf("unexpected filterEmailsByPrefix: %+v", filtered)
	}

	exSQL := exfilSignalsSQL("`p`.`region-us`", "creation_time >= TIMESTAMP_SUB(CURRENT_TIMESTAMP(), INTERVAL 7 DAY)")
	if !strings.Contains(exSQL, "QUALIFY ROW_NUMBER() OVER (PARTITION BY signal") {
		t.Errorf("exfilSignalsSQL missing per-signal QUALIFY:\n%s", exSQL)
	}
}

