package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/tabwriter"
	"time"
)

// mustTime は RFC3339 文字列を time.Time に変換する。テスト用の補助関数。
func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("failed to parse %q: %v", s, err)
	}
	return v
}

// ---------------------------------------------------------------
// CountPending
// ---------------------------------------------------------------

// CountPending の表駆動テスト。
func TestCoundPending(t *testing.T) {
	const me = "me"
	at := func(s string) time.Time { return mustTime(t, s) }

	tests := []struct {
		name   string
		events []Event
		want   int
	}{
		{
			name:   "イベントが無ければ0件",
			events: nil,
			want:   0,
		},
		{
			name: "自分のアクションが無ければ他者の全件が対象",
			events: []Event{
				{at("2026-10-01T10:00:00Z"), "alice"},
				{at("2026-10-01T11:00:00Z"), "bob"},
			},
			want: 2,
		},
		{
			name: "自分の最終アクションより前の他者は対象外",
			events: []Event{
				{at("2026-10-01T09:00:00Z"), "alice"},
				{at("2026-10-01T10:00:00Z"), me},
			},
			want: 0,
		},
		{
			name: "自分の最終アクションより後の他者のみ数える",
			events: []Event{
				{at("2026-10-01T09:00:00Z"), "alice"},
				{at("2026-10-01T10:00:00Z"), me},
				{at("2026-10-01T11:00:00Z"), "alice"},
				{at("2026-10-01T12:00:00Z"), "bot"},
			},
			want: 2,
		},
		{
			name: "自分のアクションが複数ある場合は最後のものが基準",
			events: []Event{
				{at("2026-10-01T10:00:00Z"), me},
				{at("2026-10-01T11:00:00Z"), "alice"},
				{at("2026-10-01T12:00:00Z"), me},
			},
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountPending(tt.events, me)
			if got != tt.want {
				t.Errorf("CountPending(%v, %v) = %v, want %v", tt.events, me, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------
// ParseConfig / LoadConfig
// ---------------------------------------------------------------

// 正常系: 全項目を指定した設定が読めること。
func TestParseConfigFull(t *testing.T) {
	data := []byte(`
author: alice
repositories:
  - o/r1
  - o/r2
exclude_users:
  - some-bot
`)
	cfg, err := ParseConfig(string(data))
	if err != nil {
		t.Fatalf("ParseConfig() が予期せず失敗しました: %v", err)
	}
	if cfg.Author != "alice" {
		t.Errorf("Author = %q, want alice", cfg.Author)
	}
	if len(cfg.Repositories) != 2 || cfg.Repositories[1] != "o/r2" {
		t.Errorf("Repositories = %v, want [o/r1 o/r2]", cfg.Repositories)
	}
	if len(cfg.ExcludeUsers) != 1 || cfg.ExcludeUsers[0] != "some-bot" {
		t.Errorf("ExcludeUsers = %v, want [some-bot]", cfg.ExcludeUsers)
	}
}

// author を省略したら "@me" になること。
func TestParseConfigDefaultAuthor(t *testing.T) {
	cfg, err := ParseConfig(string([]byte("repositories:\n  - o/r\n")))
	if err != nil {
		t.Fatalf("ParseConfig() が予期せず失敗しました: %v", err)
	}
	if cfg.Author != "@me" {
		t.Errorf("Author = %q, want @me", cfg.Author)
	}
}

// 異常系の表駆動テスト。
func TestParseConfigError(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{"空ファイル", ""},
		{"repositories が無い", "author: \"@me\"\n"},
		{"repositories が空配列", "repositories: []\n"},
		{"owner/repo 形式でない", "repositories:\n  - just-repo\n"},
		{"スラッシュが2つ以上", "repositories:\n  - a/b/c\n"},
		{"owner が空", "repositories:\n  - /repo\n"},
		{"未知のキー(タイプミス検知)", "repositorys:\n  - o/r\n"},
		{"YAMLとして不正", "repositories: [\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseConfig(string([]byte(tt.data))); err == nil {
				t.Errorf("エラーになるべき設定が受理されました: %q", tt.data)
			}
		})
	}
}

// LoadConfig: ファイルから読めること、存在しないファイルはエラーになること。
func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte("repositories:\n  - o/r\n"), 0o600); err != nil {
		t.Fatalf("テスト用ファイルの作成に失敗しました: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig() が予期せず失敗しました: %v", err)
	}
	if len(cfg.Repositories) != 1 || cfg.Repositories[0] != "o/r" {
		t.Errorf("Repositories = %v, want [o/r]", cfg.Repositories)
	}

	if _, err := LoadConfig(filepath.Join(dir, "missing.yaml")); err == nil {
		t.Error("存在しないファイルなのに LoadConfig() が nil を返しました")
	}
}

// ---------------------------------------------------------------
// Collect
// ---------------------------------------------------------------

// fakeRunner は gh の代わりに、引数列をキーにして固定のJSONを返す Runner。
func fakeRunner(responses map[string]string) Runner {
	return func(args ...string) ([]byte, error) {
		key := strings.Join(args, " ")
		if v, ok := responses[key]; ok {
			return []byte(v), nil
		}
		return nil, fmt.Errorf("想定外の gh 呼び出しです: %s", key)
	}
}

// Collect の正常系。
// コミット(自分,10:00) → alice のレビュー(11:00) → 自分のコメント(12:00)
// → bot のインラインコメント(13:00) なので、要対応は 1 件になる。
// インラインコメントは --paginate の「配列が連結された出力」を再現している。
func TestCollect(t *testing.T) {
	runner := fakeRunner(map[string]string{
		"pr list --repo o/r --author @me --state open --json number": `[{"number":7}]`,
		"pr view 7 --repo o/r --json title,reviewDecision,updatedAt,commits,reviews,comments": `{
			"title": "テストPR",
			"reviewDecision": "CHANGES_REQUESTED",
			"updatedAt": "2026-10-01T13:00:00Z",
			"commits": [{"committedDate": "2026-10-01T10:00:00Z"}],
			"reviews": [{"author": {"login": "alice"}, "submittedAt": "2026-10-01T11:00:00Z"}],
			"comments": [{"author": {"login": "me"}, "createdAt": "2026-10-01T12:00:00Z"}]
		}`,
		"api repos/o/r/pulls/7/comments --paginate": `[{"user":{"login":"bot"},"created_at":"2026-10-01T13:00:00Z"}][{"user":{"login":"alice"},"created_at":"2026-10-01T09:00:00Z"}]`,
	})

	got, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me")
	if err != nil {
		t.Fatalf("Collect() が予期せず失敗しました: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("PR 件数 = %d, want 1", err)
	}
	s := got[0]
	if s.Repo != "o/r" || s.Number != 7 || s.Title != "テストPR" {
		t.Errorf("リポジトリ/番号/タイトルが不正です: %+v", s)
	}
	if s.ReviewDecision != "CHANGES_REQUESTED" {
		t.Errorf("ReviewDecision = %q, want CHANGES_REQUESTED", s.ReviewDecision)
	}
	if s.Pending != 1 {
		t.Errorf("Pending = %d, want 1", s.Pending)
	}
}

// reviewDecision が空文字のとき NONE と表示されること。
func TestCollectNoReviewDecision(t *testing.T) {
	runner := fakeRunner(map[string]string{
		"pr list --repo o/r --author @me --state open --json number": `[{"number":8}]`,
		"pr view 8 --repo o/r --json title,reviewDecision,updatedAt,commits,reviews,comments": `{
			"title": "レビュー未設定",
			"reviewDecision": "",
			"updatedAt": "2026-10-01T10:00:00Z",
			"commits": [], "reviews": [], "comments": []
		}`,
		"api repos/o/r/pulls/8/comments --paginate": `[]`,
	})

	got, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me")
	if err != nil {
		t.Fatalf("Collect() が予期せず失敗しました: %v", err)
	}
	if got[0].ReviewDecision != "NONE" {
		t.Errorf("ReviewDecision = %q, want NONE", got[0].ReviewDecision)
	}
}

// exclude_users に指定したユーザーの行動は要対応に数えないこと。
func TestCollectExcludeUsers(t *testing.T) {
	responses := map[string]string{
		"pr list --repo o/r --author @me --state open --json number": `[{"number":9}]`,
		"pr view 9 --repo o/r --json title,reviewDecision,updatedAt,commits,reviews,comments": `{
			"title": "除外テスト",
			"reviewDecision": "",
			"updatedAt": "2026-10-01T12:00:00Z",
			"commits": [{"committedDate": "2026-10-01T10:00:00Z"}],
			"reviews": [{"author": {"login": "some-bot"}, "submittedAt": "2026-10-01T11:00:00Z"}],
			"comments": [{"author": {"login": "alice"}, "createdAt": "2026-10-01T12:00:00Z"}]
		}`,
		"api repos/o/r/pulls/9/comments --paginate": `[]`,
	}

	// 除外なし: some-bot と alice の2件
	got, err := Collect(fakeRunner(responses), "o/r", &Config{Author: "@me"}, "me")
	if err != nil {
		t.Fatalf("Collect() が予期せず失敗しました: %v", err)
	}
	if got[0].Pending != 2 {
		t.Errorf("除外なしの Pending = %d, want 2", got[0].Pending)
	}

	// some-bot を除外: alice の1件のみ
	cfg := &Config{Author: "@me", ExcludeUsers: []string{"some-bot"}}
	got, err = Collect(fakeRunner(responses), "o/r", cfg, "me")
	if err != nil {
		t.Fatalf("Collect() が予期せず失敗しました: %v", err)
	}
	if got[0].Pending != 1 {
		t.Errorf("除外ありの Pending = %d, want 1", got[0].Pending)
	}
}

// gh の呼び出しが失敗したらエラーが伝播すること。
func TestCollectRunnerError(t *testing.T) {
	runner := fakeRunner(map[string]string{})

	if _, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me"); err == nil {
		t.Fatal("gh が失敗したのに Collect() が nil を返しました")
	}
}

// ---------------------------------------------------------------
// 設定ファイルの探索
// ---------------------------------------------------------------

// ConfigCandidates の表駆動テスト。
// 環境変数は getenv を差し替えて注入するので、実環境に依存しない。
func TestConfigCandidates(t *testing.T) {
	tests := []struct {
		name string
		xdg  string
		home string
		want []string
	}{
		{
			name: "XDG未設定ならカレントとホームの2件",
			xdg:  "",
			home: "/home/u",
			want: []string{"config.yaml", "/home/u/.config/pr-check/config.yaml"},
		},
		{
			name: "XDG設定ありならカレント・XDG・ホームの順",
			xdg:  "/x",
			home: "/home/u",
			want: []string{
				"config.yaml",
				"/x/pr-check/config.yaml",
				"/home/u/.config/pr-check/config.yaml",
			},
		},
		{
			name: "XDGがホームの.configと同じなら重複を除く",
			xdg:  "/home/u/.config",
			home: "/home/u",
			want: []string{"config.yaml", "/home/u/.config/pr-check/config.yaml"},
		},
		{
			name: "ホーム不明ならホーム配下の候補を含まない",
			xdg:  "",
			home: "",
			want: []string{"config.yaml"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			getenv := func(key string) string {
				if key == "XDG_CONFIG_HOME" {
					return tt.xdg
				}
				return ""
			}
			got := ConfigCandidates(getenv, tt.home)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ConfigCandidates() = %v, want %v", got, tt.want)
			}
		})
	}
}

// ResolveConfigPath の正常系。
func TestResolveConfigPath(t *testing.T) {
	candidates := []string{"config.yaml", "/x/pr-check/config.yaml", "/home/u/.config/pr-check/config.yaml"}

	tests := []struct {
		name     string
		explicit string
		existing []string
		want     string
	}{
		{"明示指定があり存在する", "/my/c.yaml", []string{"/my/c.yaml", "config.yaml"}, "/my/c.yaml"},
		{"カレントに存在すれば最優先", "", []string{"config.yaml", "/home/u/.config/pr-check/config.yaml"}, "config.yaml"},
		{"カレントに無くXDGにあればXDG", "", []string{"/x/pr-check/config.yaml", "/home/u/.config/pr-check/config.yaml"}, "/x/pr-check/config.yaml"},
		{"ホームの.configのみ存在", "", []string{"/home/u/.config/pr-check/config.yaml"}, "/home/u/.config/pr-check/config.yaml"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := map[string]bool{}
			for _, p := range tt.existing {
				set[p] = true
			}
			exists := func(p string) bool { return set[p] }

			got, err := ResolveConfigPath(tt.explicit, candidates, exists)
			if err != nil {
				t.Fatalf("ResolveConfigPath() が予期せず失敗しました: %v", err)
			}
			if got != tt.want {
				t.Errorf("ResolveConfigPath() = %v, want %v", got, tt.want)
			}
		})
	}
}

// 明示指定したファイルが無い場合は、他の候補があってもフォールバックせずエラーにする。
func TestResolveConfigPathExplicitMissing(t *testing.T) {
	candidates := []string{"config.yaml"}
	exists := func(p string) bool { return p == "config.yaml" }

	_, err := ResolveConfigPath("/typo/c.yaml", candidates, exists)
	if err == nil {
		t.Fatal("明示指定のファイルが無いのにエラーになりませんでした(フォールバックしてはいけません)")
	}
	if !strings.Contains(err.Error(), "/typo/c.yaml") {
		t.Errorf("エラーに指定されたパスが含まれていません: %v", err)
	}
}

// どこにも無い場合は、探索した全パスをエラーに列挙する。
func TestResolveConfigPathNotFound(t *testing.T) {
	candidates := []string{"config.yaml", "/x/pr-check/config.yaml"}
	exists := func(string) bool { return false }

	_, err := ResolveConfigPath("", candidates, exists)
	if err == nil {
		t.Fatal("設定ファイルがどこにも無いのにエラーになりませんでした")
	}
	for _, c := range candidates {
		if !strings.Contains(err.Error(), c) {
			t.Errorf("エラーに探索パス %q が含まれていません: %v", c, err)
		}
	}
}

// ---------------------------------------------------------------
// Render
// ---------------------------------------------------------------

// testSummary はテスト用の Summary を作る。
// 時刻は time.Local で作る。Render は Local() に変換して表示するため、
// 実行環境のタイムゾーンに関わらず期待値が "2026-08-19 12:52" で固定できる。
func testSummary(repo string, num int, title, decision string, pending int) Summary {
	return Summary{
		Repo:           repo,
		Number:         num,
		Title:          title,
		ReviewDecision: decision,
		Pending:        pending,
		UpdatedAt:      time.Date(2026, 8, 19, 12, 52, 0, 0, time.Local),
	}
}

// TestRenderGolden は ASCII のみの入力に対して、出力を完全一致で確認する。
func TestRenderGolden(t *testing.T) {
	var buf bytes.Buffer
	err := Render(&buf, []Summary{testSummary("a/b", 8, "fix", "NONE", 1)})
	if err != nil {
		t.Fatalf("Render がエラーを返しました: %v", err)
	}

	want := "リポジトリ  PR  タイトル  レビュー  要対応  最終更新\n" +
		"a/b    8   fix   NONE  1    2026-08-19 12:52\n"
	if got := buf.String(); got != want {
		t.Errorf("出力が一致しません。\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderEmpty は0件のとき、ヘッダ行だけが出力されることを確認する。
func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, nil); err != nil {
		t.Fatalf("Render がエラーを返しました: %v", err)
	}
	want := "リポジトリ  PR  タイトル  レビュー  要対応  最終更新\n"
	if got := buf.String(); got != want {
		t.Errorf("出力が一致しません。\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderMatchesTabwriter は、従来の tabwriter 実装と出力が完全に同じことを確認する。
// 日本語タイトルや長さの異なる行を混ぜ、「見た目が変わっていない」ことを保証する回帰テスト。
func TestRenderMatchesTabwriter(t *testing.T) {
	summaries := []Summary{
		testSummary("jacoloves/jiyucho", 8, "search: ワークスペース横断検索を追加する", "NONE", 1),
		testSummary("jacoloves/pr-check", 123, "feat: config discovery", "APPROVED", 0),
		testSummary("x/y", 2, "短い", "CHANGES_REQUESTED", 12),
	}

	// 期待値: 従来の run() と同じ書き方で tabwriter に書かせる。
	var want bytes.Buffer
	w := tabwriter.NewWriter(&want, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "リポジトリ\tPR\tタイトル\tレビュー\t要対応\t最終更新")
	for _, s := range summaries {
		fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\t%s\n",
			s.Repo, s.Number, s.Title, s.ReviewDecision, s.Pending, s.UpdatedAt.Local().Format("2006-01-02 15:04"))
	}
	if err := w.Flush(); err != nil {
		t.Fatalf("tabwriter の Flush に失敗しました: %v", err)
	}

	var got bytes.Buffer
	if err := Render(&got, summaries); err != nil {
		t.Fatalf("Render がエラーを返しました: %v", err)
	}
	if got.String() != want.String() {
		t.Errorf("tabwriter の出力と一致しません。\n got:\n%s\nwant:\n%s", got.String(), want.String())
	}
}

// failWriter は常に書き込みに失敗する io.Writer。
type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) {
	return 0, errors.New("書き込み失敗(テスト用)")
}

// TestRenderWriteError は書き込み失敗がエラーとして返ることを確認する。
func TestRenderWriteError(t *testing.T) {
	err := Render(failWriter{}, []Summary{testSummary("a/b", 1, "t", "NONE", 0)})
	if err == nil {
		t.Fatal("書き込み失敗なのにエラーが返りませんでした")
	}
	if !strings.Contains(err.Error(), "書き込み失敗") {
		t.Errorf("エラーに原因が含まれていません: %v", err)
	}
}

// ---------------------------------------------------------------
// ソート
// ---------------------------------------------------------------

// sortFixture はソートのテスト用に、(repo, number, 判定, 要対応, 更新日) を持つ Summary を作る。
func sortFixture(repo string, num int, decision string, pending, day int) Summary {
	return Summary{
		Repo:           repo,
		Number:         num,
		ReviewDecision: decision,
		Pending:        pending,
		UpdatedAt:      time.Date(2026, 8, day, 12, 0, 0, 0, time.UTC),
	}
}

// sortInput はソートのテストで共通に使う入力。毎回新しく作り、テスト間で共有しない
// (SortSummaries は引数のスライスを直接並べ替えるため)。
func sortInput() []Summary {
	return []Summary{
		sortFixture("a/x", 3, "APPROVED", 0, 10),
		sortFixture("a/x", 1, "CHANGES_REQUESTED", 2, 12),
		sortFixture("b/y", 2, "REVIEW_REQUIRED", 2, 11),
		sortFixture("b/y", 1, "NONE", 5, 13),
		sortFixture("a/z", 7, "UNKNOWN", 1, 9),
	}
}

// ids は並び順の確認用に "repo#number" の配列にする。
func ids(ss []Summary) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = fmt.Sprintf("%s#%d", s.Repo, s.Number)
	}
	return out
}

func TestParseSortKey(t *testing.T) {
	for _, in := range []string{"pending", "updated", "review", "repo", "number"} {
		got, err := ParseSortKey(in)
		if err != nil {
			t.Errorf("ParseSortKey(%q) が予期せずエラー: %v", in, err)
		}
		if string(got) != in {
			t.Errorf("ParseSortKey(%q) = %q, want %q", in, got, in)
		}
	}

	// 空文字は「ソートしない」を表す。
	got, err := ParseSortKey("")
	if err != nil || got != SortNone {
		t.Errorf("ParseSortKey(\"\") = (%q, %v), want (SortNone, nil)", got, err)
	}
}

func TestParseSortKeyError(t *testing.T) {
	_, err := ParseSortKey("foo")
	if err == nil {
		t.Fatal("不正なキーなのにエラーが返りませんでした")
	}
	// 入力値と、使えるキー一覧がメッセージに含まれること。
	for _, want := range []string{"foo", "pending", "updated", "review", "repo", "number"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("エラーに %q が含まれていません: %v", want, err)
		}
	}
}

func TestSortSummaries(t *testing.T) {
	tests := []struct {
		name    string
		key     SortKey
		reverse bool
		want    []string
	}{
		{
			name: "pending: 要対応が多い順、同数なら更新が新しい順",
			key:  SortPending,
			want: []string{"b/y#1", "a/x#1", "b/y#2", "a/z#7", "a/x#3"},
		},
		{
			name: "updated: 更新が新しい順",
			key:  SortUpdated,
			want: []string{"b/y#1", "a/x#1", "b/y#2", "a/x#3", "a/z#7"},
		},
		{
			name: "review: 緊急度順、未知の値は最後",
			key:  SortReview,
			want: []string{"a/x#1", "b/y#2", "b/y#1", "a/x#3", "a/z#7"},
		},
		{
			name: "repo: リポジトリ昇順、同じなら番号昇順",
			key:  SortRepo,
			want: []string{"a/x#1", "a/x#3", "a/z#7", "b/y#1", "b/y#2"},
		},
		{
			name: "number: 番号昇順、同じならリポジトリ昇順",
			key:  SortNumber,
			want: []string{"a/x#1", "b/y#1", "b/y#2", "a/x#3", "a/z#7"},
		},
		{
			name:    "pending + reverse: 完全に逆順",
			key:     SortPending,
			reverse: true,
			want:    []string{"a/x#3", "a/z#7", "b/y#2", "a/x#1", "b/y#1"},
		},
		{
			name:    "updated + reverse: 更新が古い順",
			key:     SortUpdated,
			reverse: true,
			want:    []string{"a/z#7", "a/x#3", "b/y#2", "a/x#1", "b/y#1"},
		},
		{
			name: "キー無し: 入力順のまま",
			key:  SortNone,
			want: []string{"a/x#3", "a/x#1", "b/y#2", "b/y#1", "a/z#7"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := sortInput()
			SortSummaries(in, tt.key, tt.reverse)
			if got := ids(in); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("並び順が一致しません。\n got: %v\nwant: %v", got, tt.want)
			}
		})
	}
}

// TestSortSummariesStable は、比較上まったく同点の要素が元の順序を保つことを確認する。
// pending と updated が同じなら、pending ソートでは元の並びが維持されるはず。
func TestSortSummariesStable(t *testing.T) {
	in := []Summary{
		sortFixture("b/y", 9, "None", 1, 5),
		sortFixture("a/x", 9, "None", 1, 5),
	}
	SortSummaries(in, SortPending, false)
	want := []string{"b/y#9", "a/x#9"}
	if got := ids(in); !reflect.DeepEqual(got, want) {
		t.Errorf("安定ソートになっていません。\n got: %v\nwant: %v", got, want)
	}

	// reverse でも完全同点の要素は元の順序を保つ(単純な逆順にはならない)。
	SortSummaries(in, SortPending, true)
	if got := ids(in); !reflect.DeepEqual(got, want) {
		t.Errorf("reverse でも同点は元の順序を保つべきです。\n got: %v\nwant: %v", got, want)
	}
}
