package db

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/majiayu000/auto-contributor/pkg/models"
)

func TestCreatePullRequestPopulatesIDAndSupportsUpdateSQLite(t *testing.T) {
	db := newSQLiteTestDB(t)
	testCreatePullRequestPopulatesIDAndSupportsUpdate(t, db)
}

func TestCreatePullRequestUsesReturningIDOnPostgres(t *testing.T) {
	stub := &createPullRequestPostgresStub{nextID: 41}
	registerCreatePullRequestPostgresStub(t, stub)

	sqlDB, err := sql.Open(createPullRequestPostgresStubDriverName, "")
	if err != nil {
		t.Fatalf("open stub postgres db: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db := &DB{DB: sqlDB, dbType: DBTypePostgres}
	pr := &models.PullRequest{
		IssueID:    17,
		PRURL:      "https://example.com/owner/repo/pull/17",
		PRNumber:   17,
		BranchName: "feat/postgres-returning-id",
		Status:     models.PRStatusDraft,
		CIStatus:   "pending",
	}

	if err := db.CreatePullRequest(pr); err != nil {
		t.Fatalf("create pull request: %v", err)
	}
	if pr.ID != stub.nextID {
		t.Fatalf("pull request ID = %d, want %d", pr.ID, stub.nextID)
	}
	if stub.queryCount != 1 {
		t.Fatalf("QueryContext count = %d, want 1", stub.queryCount)
	}
	if stub.execCount != 0 {
		t.Fatalf("ExecContext count = %d, want 0", stub.execCount)
	}
	if stub.lastQuery != "INSERT INTO pull_requests (issue_id, pr_url, pr_number, branch_name, status, ci_status) VALUES ($1, $2, $3, $4, $5, $6) RETURNING id" {
		t.Fatalf("query = %q", stub.lastQuery)
	}
	if len(stub.lastArgs) != 6 {
		t.Fatalf("arg count = %d, want 6", len(stub.lastArgs))
	}
	if got := stub.lastArgs[0]; got != pr.IssueID {
		t.Fatalf("issue_id arg = %v, want %d", got, pr.IssueID)
	}
	if got := stub.lastArgs[1]; got != pr.PRURL {
		t.Fatalf("pr_url arg = %v, want %q", got, pr.PRURL)
	}
	if got := stub.lastArgs[2]; got != int64(pr.PRNumber) {
		t.Fatalf("pr_number arg = %v, want %d", got, pr.PRNumber)
	}
	if got := stub.lastArgs[3]; got != pr.BranchName {
		t.Fatalf("branch_name arg = %v, want %q", got, pr.BranchName)
	}
	if got := stub.lastArgs[4]; got != string(pr.Status) {
		t.Fatalf("status arg = %v, want %q", got, pr.Status)
	}
	if got := stub.lastArgs[5]; got != pr.CIStatus {
		t.Fatalf("ci_status arg = %v, want %q", got, pr.CIStatus)
	}
}

func TestCreatePullRequestPopulatesIDAndSupportsUpdatePostgres(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	if os.Getenv("RUN_POSTGRES_INTEGRATION_TESTS") == "" {
		t.Skip("RUN_POSTGRES_INTEGRATION_TESTS is not set")
	}

	db, err := NewWithURL(databaseURL, filepath.Join(t.TempDir(), "unused.db"))
	if err != nil {
		t.Fatalf("create postgres test db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})

	testCreatePullRequestPopulatesIDAndSupportsUpdate(t, db)
}

func testCreatePullRequestPopulatesIDAndSupportsUpdate(t *testing.T, db *DB) {

	uniqueSuffix := time.Now().UnixNano()
	repo := fmt.Sprintf("owner/repo-%d", uniqueSuffix)
	issueNumber := int(uniqueSuffix % 1000000)
	prNumber := issueNumber
	branchName := fmt.Sprintf("feat/issue-%d", uniqueSuffix)
	prURL := fmt.Sprintf("https://example.com/%s/pull/%d", repo, prNumber)

	t.Helper()

	if db.IsPostgres() {
		t.Cleanup(func() {
			if _, err := db.Exec("DELETE FROM pull_requests WHERE pr_url = $1", prURL); err != nil {
				t.Fatalf("cleanup pull_requests: %v", err)
			}
			if _, err := db.Exec("DELETE FROM issues WHERE repo = $1 AND issue_number = $2", repo, issueNumber); err != nil {
				t.Fatalf("cleanup issues: %v", err)
			}
		})
	}

	issue := &models.Issue{
		Repo:            repo,
		IssueNumber:     issueNumber,
		Title:           "Populate PR primary key",
		Body:            "Regression coverage for PR inserts",
		Labels:          "bug",
		Language:        "Go",
		DifficultyScore: 0.5,
		Status:          models.IssueStatusDiscovered,
	}
	if err := db.CreateIssue(issue); err != nil {
		t.Fatalf("create issue: %v", err)
	}
	if issue.ID <= 0 {
		t.Fatalf("issue ID = %d, want > 0", issue.ID)
	}

	pr := &models.PullRequest{
		IssueID:    issue.ID,
		PRURL:      prURL,
		PRNumber:   prNumber,
		BranchName: branchName,
		Status:     models.PRStatusDraft,
		CIStatus:   "pending",
	}
	if err := db.CreatePullRequest(pr); err != nil {
		t.Fatalf("create pull request: %v", err)
	}
	if pr.ID <= 0 {
		t.Fatalf("pull request ID = %d, want > 0", pr.ID)
	}

	if err := db.UpdatePRStatus(pr.ID, models.PRStatusOpen); err != nil {
		t.Fatalf("update PR status: %v", err)
	}
	if err := db.UpdatePRFeedbackCheck(pr.ID, 2); err != nil {
		t.Fatalf("update PR feedback check: %v", err)
	}

	stored, err := db.getPRByID(pr.ID)
	if err != nil {
		t.Fatalf("get PR by ID: %v", err)
	}
	if stored.Status != models.PRStatusOpen {
		t.Fatalf("stored status = %q, want %q", stored.Status, models.PRStatusOpen)
	}
	if stored.FeedbackRound != 2 {
		t.Fatalf("stored feedback round = %d, want %d", stored.FeedbackRound, 2)
	}
	if stored.LastFeedbackCheckAt == nil {
		t.Fatal("stored last feedback check timestamp is nil")
	}
}

func newSQLiteTestDB(t *testing.T) *DB {
	t.Helper()

	db, err := New(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("create sqlite test db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

const createPullRequestPostgresStubDriverName = "create_pull_request_postgres_stub"

var createPullRequestPostgresStubValue atomic.Pointer[createPullRequestPostgresStub]

func init() {
	sql.Register(createPullRequestPostgresStubDriverName, createPullRequestPostgresStubDriver{})
}

func registerCreatePullRequestPostgresStub(t *testing.T, stub *createPullRequestPostgresStub) {
	t.Helper()
	createPullRequestPostgresStubValue.Store(stub)
	t.Cleanup(func() {
		createPullRequestPostgresStubValue.Store(nil)
	})
}

type createPullRequestPostgresStub struct {
	nextID     int64
	queryCount int
	execCount  int
	lastQuery  string
	lastArgs   []driver.Value
}

type createPullRequestPostgresStubDriver struct{}

func (createPullRequestPostgresStubDriver) Open(string) (driver.Conn, error) {
	stub := createPullRequestPostgresStubValue.Load()
	if stub == nil {
		return nil, fmt.Errorf("postgres stub not registered")
	}
	return &createPullRequestPostgresStubConn{stub: stub}, nil
}

type createPullRequestPostgresStubConn struct {
	stub *createPullRequestPostgresStub
}

func (c *createPullRequestPostgresStubConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not implemented")
}

func (c *createPullRequestPostgresStubConn) Close() error {
	return nil
}

func (c *createPullRequestPostgresStubConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions not implemented")
}

func (c *createPullRequestPostgresStubConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.stub.queryCount++
	c.stub.lastQuery = normalizeWhitespace(query)
	c.stub.lastArgs = namedValuesToValues(args)
	return &createPullRequestPostgresStubRows{
		columns: []string{"id"},
		rows:    [][]driver.Value{{c.stub.nextID}},
	}, nil
}

func (c *createPullRequestPostgresStubConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	c.stub.execCount++
	return nil, fmt.Errorf("unexpected ExecContext call")
}

type createPullRequestPostgresStubRows struct {
	columns []string
	rows    [][]driver.Value
	index   int
}

func (r *createPullRequestPostgresStubRows) Columns() []string {
	return r.columns
}

func (r *createPullRequestPostgresStubRows) Close() error {
	return nil
}

func (r *createPullRequestPostgresStubRows) Next(dest []driver.Value) error {
	if r.index >= len(r.rows) {
		return io.EOF
	}
	copy(dest, r.rows[r.index])
	r.index++
	return nil
}

func normalizeWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func namedValuesToValues(args []driver.NamedValue) []driver.Value {
	values := make([]driver.Value, len(args))
	for i, arg := range args {
		values[i] = arg.Value
	}
	return values
}

var _ driver.Conn = (*createPullRequestPostgresStubConn)(nil)
var _ driver.QueryerContext = (*createPullRequestPostgresStubConn)(nil)
var _ driver.ExecerContext = (*createPullRequestPostgresStubConn)(nil)
var _ driver.Rows = (*createPullRequestPostgresStubRows)(nil)

func TestListRepoProfilesNoFilterSQLite(t *testing.T) {
	db := newSQLiteTestDB(t)

	mustUpsertRepoProfile(t, db, "owner/alpha", 0.75, false)
	mustUpsertRepoProfile(t, db, "owner/beta", 0.50, true)

	profiles, err := db.ListRepoProfiles("")
	if err != nil {
		t.Fatalf("list repo profiles: %v", err)
	}
	if len(profiles) != 2 {
		t.Fatalf("profile count = %d, want 2", len(profiles))
	}
	if profiles[0].Repo != "owner/alpha" || profiles[1].Repo != "owner/beta" {
		t.Fatalf("profiles = %#v", []string{profiles[0].Repo, profiles[1].Repo})
	}
}

func TestListRepoProfilesFilterSQLite(t *testing.T) {
	db := newSQLiteTestDB(t)

	mustUpsertRepoProfile(t, db, "owner/alpha", 0.75, false)
	mustUpsertRepoProfile(t, db, "other/project", 0.25, false)

	profiles, err := db.ListRepoProfiles("%owner/alpha%")
	if err != nil {
		t.Fatalf("list repo profiles: %v", err)
	}
	if len(profiles) != 1 {
		t.Fatalf("profile count = %d, want 1", len(profiles))
	}
	if profiles[0].Repo != "owner/alpha" {
		t.Fatalf("repo = %q, want %q", profiles[0].Repo, "owner/alpha")
	}
}

func TestGetBlacklistNoFilterSQLite(t *testing.T) {
	db := newSQLiteTestDB(t)

	mustAddBlacklistEntry(t, db, "owner/alpha", "first")
	mustAddBlacklistEntry(t, db, "owner/beta", "second")

	entries, err := db.GetBlacklistFiltered("")
	if err != nil {
		t.Fatalf("get blacklist: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(entries))
	}
}

func TestGetBlacklistFilterSQLite(t *testing.T) {
	db := newSQLiteTestDB(t)

	mustAddBlacklistEntry(t, db, "owner/alpha", "first")
	mustAddBlacklistEntry(t, db, "other/project", "second")

	entries, err := db.GetBlacklistFiltered("%owner/alpha%")
	if err != nil {
		t.Fatalf("get blacklist: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(entries))
	}
	if entries[0].Repo != "owner/alpha" {
		t.Fatalf("repo = %q, want %q", entries[0].Repo, "owner/alpha")
	}
}

func TestListRepoProfilesUsesParameterizedLikePostgres(t *testing.T) {
	stub := &filterQueryPostgresStub{
		columns: []string{
			"repo", "total_prs_submitted", "total_merged", "total_rejected", "merge_rate",
			"avg_response_time_hours", "requires_cla", "requires_assignment", "preferred_pr_size",
			"blacklisted", "blacklist_reason", "cooldown_until", "strategy_notes", "last_interaction",
			"updated_at",
		},
		rows: [][]driver.Value{},
	}
	registerFilterQueryPostgresStub(t, stub)

	sqlDB, err := sql.Open(filterQueryPostgresStubDriverName, "")
	if err != nil {
		t.Fatalf("open stub postgres db: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db := &DB{DB: sqlDB, dbType: DBTypePostgres}
	payload := "foo%' OR 1=1 --"

	profiles, err := db.ListRepoProfiles(payload)
	if err != nil {
		t.Fatalf("list repo profiles: %v", err)
	}
	if len(profiles) != 0 {
		t.Fatalf("profile count = %d, want 0", len(profiles))
	}
	if stub.lastQuery != "SELECT repo, total_prs_submitted, total_merged, total_rejected, merge_rate, avg_response_time_hours, requires_cla, requires_assignment, preferred_pr_size, blacklisted, blacklist_reason, cooldown_until, strategy_notes, last_interaction, updated_at FROM repo_profiles WHERE repo LIKE $1 ORDER BY repo" {
		t.Fatalf("query = %q", stub.lastQuery)
	}
	if len(stub.lastArgs) != 1 || stub.lastArgs[0] != payload {
		t.Fatalf("args = %#v, want [%q]", stub.lastArgs, payload)
	}
}

func TestGetBlacklistUsesParameterizedLikePostgres(t *testing.T) {
	stub := &filterQueryPostgresStub{
		columns: []string{"id", "repo", "reason", "added_at"},
		rows:    [][]driver.Value{},
	}
	registerFilterQueryPostgresStub(t, stub)

	sqlDB, err := sql.Open(filterQueryPostgresStubDriverName, "")
	if err != nil {
		t.Fatalf("open stub postgres db: %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})

	db := &DB{DB: sqlDB, dbType: DBTypePostgres}
	payload := "foo%' OR 1=1 --"

	entries, err := db.GetBlacklistFiltered(payload)
	if err != nil {
		t.Fatalf("get blacklist: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("entry count = %d, want 0", len(entries))
	}
	if stub.lastQuery != "SELECT id, repo, reason, added_at FROM blacklist WHERE repo LIKE $1 ORDER BY added_at DESC" {
		t.Fatalf("query = %q", stub.lastQuery)
	}
	if len(stub.lastArgs) != 1 || stub.lastArgs[0] != payload {
		t.Fatalf("args = %#v, want [%q]", stub.lastArgs, payload)
	}
}

func mustUpsertRepoProfile(t *testing.T, db *DB, repo string, mergeRate float64, blacklisted bool) {
	t.Helper()
	if err := db.UpsertRepoProfile(&models.RepoProfile{
		Repo:              repo,
		TotalPRsSubmitted: 4,
		TotalMerged:       3,
		TotalRejected:     1,
		MergeRate:         mergeRate,
		Blacklisted:       blacklisted,
	}); err != nil {
		t.Fatalf("upsert repo profile %q: %v", repo, err)
	}
}

func mustAddBlacklistEntry(t *testing.T, db *DB, repo, reason string) {
	t.Helper()
	if err := db.AddToBlacklist(repo, reason); err != nil {
		t.Fatalf("add blacklist entry %q: %v", repo, err)
	}
}

const filterQueryPostgresStubDriverName = "filter_query_postgres_stub"

var filterQueryPostgresStubValue atomic.Pointer[filterQueryPostgresStub]

func init() {
	sql.Register(filterQueryPostgresStubDriverName, filterQueryPostgresStubDriver{})
}

func registerFilterQueryPostgresStub(t *testing.T, stub *filterQueryPostgresStub) {
	t.Helper()
	filterQueryPostgresStubValue.Store(stub)
	t.Cleanup(func() {
		filterQueryPostgresStubValue.Store(nil)
	})
}

type filterQueryPostgresStub struct {
	columns    []string
	rows       [][]driver.Value
	queryCount int
	lastQuery  string
	lastArgs   []driver.Value
}

type filterQueryPostgresStubDriver struct{}

func (filterQueryPostgresStubDriver) Open(string) (driver.Conn, error) {
	stub := filterQueryPostgresStubValue.Load()
	if stub == nil {
		return nil, fmt.Errorf("filter query postgres stub not registered")
	}
	return &filterQueryPostgresStubConn{stub: stub}, nil
}

type filterQueryPostgresStubConn struct {
	stub *filterQueryPostgresStub
}

func (c *filterQueryPostgresStubConn) Prepare(string) (driver.Stmt, error) {
	return nil, fmt.Errorf("prepare not implemented")
}

func (c *filterQueryPostgresStubConn) Close() error {
	return nil
}

func (c *filterQueryPostgresStubConn) Begin() (driver.Tx, error) {
	return nil, fmt.Errorf("transactions not implemented")
}

func (c *filterQueryPostgresStubConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	c.stub.queryCount++
	c.stub.lastQuery = normalizeWhitespace(query)
	c.stub.lastArgs = namedValuesToValues(args)
	return &createPullRequestPostgresStubRows{
		columns: c.stub.columns,
		rows:    c.stub.rows,
	}, nil
}

func (c *filterQueryPostgresStubConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	return nil, fmt.Errorf("unexpected ExecContext call")
}

var _ driver.Conn = (*filterQueryPostgresStubConn)(nil)
var _ driver.QueryerContext = (*filterQueryPostgresStubConn)(nil)
var _ driver.ExecerContext = (*filterQueryPostgresStubConn)(nil)
