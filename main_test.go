package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/mattn/go-runewidth"
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
		"pr list --repo o/r --author @me --state open --limit 200 --json number,updatedAt": `[{"number":7, "updatedAt":"2026-10-01T13:00:00Z"}]`,
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

	got, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me", time.Time{})
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
		"pr list --repo o/r --author @me --state open --limit 200 --json number,updatedAt": `[{"number":8, "updatedAt":"2026-10-01T12:00:00Z"}]`,
		"pr view 8 --repo o/r --json title,reviewDecision,updatedAt,commits,reviews,comments": `{
			"title": "レビュー未設定",
			"reviewDecision": "",
			"updatedAt": "2026-10-01T10:00:00Z",
			"commits": [], "reviews": [], "comments": []
		}`,
		"api repos/o/r/pulls/8/comments --paginate": `[]`,
	})

	got, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me", time.Time{})
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
		"pr list --repo o/r --author @me --state open --limit 200 --json number,updatedAt": `[{"number":9, "updatedAt":"2026-10-01T12:00:00Z"}]`,
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
	got, err := Collect(fakeRunner(responses), "o/r", &Config{Author: "@me"}, "me", time.Time{})
	if err != nil {
		t.Fatalf("Collect() が予期せず失敗しました: %v", err)
	}
	if got[0].Pending != 2 {
		t.Errorf("除外なしの Pending = %d, want 2", got[0].Pending)
	}

	// some-bot を除外: alice の1件のみ
	cfg := &Config{Author: "@me", ExcludeUsers: []string{"some-bot"}}
	got, err = Collect(fakeRunner(responses), "o/r", cfg, "me", time.Time{})
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

	if _, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me", time.Time{}); err == nil {
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
	err := Render(&buf, []Summary{testSummary("a/b", 8, "fix", "NONE", 1)}, NewPainter(false))
	if err != nil {
		t.Fatalf("Render がエラーを返しました: %v", err)
	}

	// ヘッダは全角なので、幅は表示幅(全角=2)で揃う。
	want := "リポジトリ  PR  タイトル  レビュー  要対応  最終更新\n" +
		"a/b         8   fix       NONE      1       2026-08-19 12:52\n"
	if got := buf.String(); got != want {
		t.Errorf("出力が一致しません。\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderEmpty は0件のとき、ヘッダ行だけが出力されることを確認する。
func TestRenderEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, nil, NewPainter(false)); err != nil {
		t.Fatalf("Render がエラーを返しました: %v", err)
	}
	want := "リポジトリ  PR  タイトル  レビュー  要対応  最終更新\n"
	if got := buf.String(); got != want {
		t.Errorf("出力が一致しません。\n got: %q\nwant: %q", got, want)
	}
}

// TestRenderAlignsByDisplayWidth は、全角文字(幅2)を含んでも、
// 最終列の開始位置が全行で同じ表示幅になること(=端末で列が揃うこと)を確認する。
// 従来は tabwriter(文字数で桁を数える)と一致することを検証していたが、
// 全角対応により意図的に仕様が変わったため、表示幅で検証する。
func TestRenderAlignsByDisplayWidth(t *testing.T) {
	summaries := []Summary{
		testSummary("jacoloves/jiyucho", 8, "search: ワークスペース横断検索を追加する", "NONE", 1),
		testSummary("jacoloves/pr-check", 123, "feat: config discovery", "APPROVED", 0),
		testSummary("x/y", 2, "短い", "CHANGES_REQUESTED", 12),
	}

	var got bytes.Buffer
	if err := Render(&got, summaries, NewPainter(false)); err != nil {
		t.Fatalf("Render がエラーを返しました: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(got.String(), "\n"), "\n")
	if len(lines) != len(summaries)+1 {
		t.Fatalf("行数が想定と違います: got %d, want %d", len(lines), len(summaries)+1)
	}

	// 最終列(ヘッダは「最終更新」、本文は日付)より前の表示幅が全行で等しいはず。
	lastCols := []string{"最終更新", "2026-08-19 12:52", "2026-08-19 12:52", "2026-08-19 12:52"}
	wantWidth := -1
	for i, line := range lines {
		if !strings.HasSuffix(line, lastCols[i]) {
			t.Fatalf("%d 行目が最終列 %q で終わっていません: %q", i, lastCols[i], line)
		}
		w := runewidth.StringWidth(strings.TrimSuffix(line, lastCols[i]))
		if wantWidth < 0 {
			wantWidth = w
		} else if w != wantWidth {
			t.Errorf("%d 行目の最終列の開始位置が揃っていません: got %d, want %d\n%s", i, w, wantWidth, got.String())
		}
	}
}

// failWriter は常に書き込みに失敗する io.Writer。
type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) {
	return 0, errors.New("書き込み失敗(テスト用)")
}

// TestRenderWriteError は書き込み失敗がエラーとして返ることを確認する。
func TestRenderWriteError(t *testing.T) {
	err := Render(failWriter{}, []Summary{testSummary("a/b", 1, "t", "NONE", 0)}, NewPainter(false))
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

// ---------------------------------------------------------------
// 色付け
// ---------------------------------------------------------------

func TestPainter(t *testing.T) {
	on := NewPainter(true)
	off := NewPainter(false)

	tests := []struct {
		name string
		got  string
		want string
	}{
		{"Bold", on.Bold("x"), "\x1b[1mx\x1b[0m"},
		{"Faint", on.Faint("x"), "\x1b[2mx\x1b[0m"},
		{"Red", on.Red("x"), "\x1b[31mx\x1b[0m"},
		{"Green", on.Green("x"), "\x1b[32mx\x1b[0m"},
		{"Yellow", on.Yellow("x"), "\x1b[33mx\x1b[0m"},
		{"BoldYellow", on.BoldYellow("x"), "\x1b[1;33mx\x1b[0m"},
		// 無効なときは全て恒等変換(何も付けない)。
		{"off Bold", off.Bold("x"), "x"},
		{"off Red", off.Red("x"), "x"},
		{"off BoldYellow", off.BoldYellow("x"), "x"},
	}
	for _, tt := range tests {
		if tt.got != tt.want {
			t.Errorf("%s: got %q, want %q", tt.name, tt.got, tt.want)
		}
	}
}

func TestShouldColor(t *testing.T) {
	env := func(m map[string]string) func(string) string {
		return func(k string) string { return m[k] }
	}

	tests := []struct {
		name  string
		mode  string
		env   map[string]string
		isTTY bool
		want  bool
	}{
		{"auto: 端末なら色あり", "auto", map[string]string{"TERM": "xterm"}, true, true},
		{"auto: NO_COLOR があれば色なし", "auto", map[string]string{"TERM": "xterm", "NO_COLOR": "1"}, true, false},
		{"auto: NO_COLOR が空なら無視", "auto", map[string]string{"TERM": "xterm", "NO_COLOR": ""}, true, true},
		{"auto: TERM=dumb なら色なし", "auto", map[string]string{"TERM": "dumb"}, true, false},
		{"auto: 端末でなければ色なし", "auto", map[string]string{"TERM": "xterm"}, false, false},
		{"always: 全てを無視して色あり", "always", map[string]string{"NO_COLOR": "1", "TERM": "dumb"}, false, true},
		{"never: 端末でも色なし", "never", map[string]string{"TERM": "xterm"}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ShouldColor(tt.mode, env(tt.env), tt.isTTY)
			if err != nil {
				t.Fatalf("予期せぬエラー: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldColorError(t *testing.T) {
	_, err := ShouldColor("rainbow", func(string) string { return "" }, true)
	if err == nil {
		t.Fatal("不正な mode なのにエラーが返りませんでした")
	}
	for _, want := range []string{"rainbow", "auto", "always", "never"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("エラーに %q が含まれていません: %v", want, err)
		}
	}
}

// colorFixture は色付けのテストで使う入力。判定4種と、要対応 0 / 1 / 12 を含む。
func colorFixture() []Summary {
	return []Summary{
		testSummary("a/b", 8, "fix", "APPROVED", 0),
		testSummary("c/dd", 1, "ドキュメント修正", "CHANGES_REQUESTED", 12),
		testSummary("e/f", 2, "u", "REVIEW_REQUIRED", 1),
		testSummary("g/h", 3, "v", "NONE", 0),
	}
}

// ansiPattern は SGR(色・装飾)のエスケープシーケンスにマッチする。
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// TestRenderNoColorHasNoEscape は、色無効のとき ANSI が一切出ないことを確認する。
func TestRenderNoColorHasNoEscape(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, colorFixture(), NewPainter(false)); err != nil {
		t.Fatalf("Render がエラーを返しました: %v", err)
	}
	if strings.Contains(buf.String(), "\x1b") {
		t.Errorf("色無効なのにエスケープが含まれています: %q", buf.String())
	}
}

// TestRenderColorKeepsLayout は、色有効の出力から ANSI を取り除くと、
// 色無効の出力と完全に一致すること(=列がずれないこと)を確認する。最も重要な回帰テスト。
func TestRenderColorKeepsLayout(t *testing.T) {
	var plain, colored bytes.Buffer
	if err := Render(&plain, colorFixture(), NewPainter(false)); err != nil {
		t.Fatal(err)
	}
	if err := Render(&colored, colorFixture(), NewPainter(true)); err != nil {
		t.Fatal(err)
	}

	stripped := ansiPattern.ReplaceAllString(colored.String(), "")
	if stripped != plain.String() {
		t.Errorf("ANSI を除去しても色なしの出力と一致しません。\n stripped:\n%s\n plain:\n%s", stripped, plain.String())
	}
}

// TestRenderColorStyles は、どのセルにどの色が付くかを確認する。
func TestRenderColorStyles(t *testing.T) {
	var buf bytes.Buffer
	if err := Render(&buf, colorFixture(), NewPainter(true)); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	wants := []string{
		"\x1b[1mPR\x1b[0m",                 // ヘッダは太字
		"\x1b[32mAPPROVED\x1b[0m",          // 承認は緑
		"\x1b[31mCHANGES_REQUESTED\x1b[0m", // 変更要求は赤
		"\x1b[33mREVIEW_REQUIRED\x1b[0m",   // レビュー待ちは黄
		"\x1b[2mNONE\x1b[0m",               // NONE は暗色
		"\x1b[2m0\x1b[0m",                  // 要対応 0 は暗色
		"\x1b[1;33m1\x1b[0m",               // 要対応 1 は太字の黄
		"\x1b[1;33m12\x1b[0m",              // 要対応 12 も太字の黄
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("出力に %q が含まれていません。\n%s", w, out)
		}
	}
}

// ---------------------------------------------------------------
// --since
// ---------------------------------------------------------------

func TestParseSince(t *testing.T) {
	// 現在時刻を固定する(UTC にして、テスト環境のタイムゾーンに依存させない)。
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		in   string
		want time.Time
	}{
		{"7d", time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)},
		{"1d", time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)},
		{"2w", time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)},
		{"36h", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)},
		{"90m", time.Date(2026, 10, 11, 10, 30, 0, 0, time.UTC)},
		{"1h30m", time.Date(2026, 10, 11, 10, 30, 0, 0, time.UTC)},
		// 日付指定は now のタイムゾーンでの 00:00。
		{"2026-10-01", time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)},
		// 未来の日付はここではエラーにしない(警告は呼び出し側の責務)。
		{"2026-12-31", time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)},
		// 前後の空白は無視する。
		{" 7d ", time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseSince(tt.in, now)
			if err != nil {
				t.Fatalf("ParseSince(%q) が予期せずエラー: %v", tt.in, err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("ParseSince(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSinceError(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	for _, in := range []string{
		"", "abc", "d", "w", "3x", "1.5d", "-1d", "0d", "0w", "0h", "-3h",
		"2026-13-01", "2026/10/01", "99999999999999999999d", "40000d",
	} {
		t.Run(in, func(t *testing.T) {
			if _, err := ParseSince(in, now); err == nil {
				t.Errorf("ParseSince(%q) がエラーを返しませんでした", in)
			}
		})
	}
}

// TestParseSinceErrorMessage は、エラーが許容する形式を案内していることを確認する。
func TestParseSinceErrorMessage(t *testing.T) {
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

	_, err := ParseSince("abc", now)
	if err == nil {
		t.Fatal("エラーが返りませんでした")
	}
	for _, want := range []string{"abc", "7d", "2w", "36h", "2026-10-01"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("エラーに %q が含まれていません: %v", want, err)
		}
	}
}

// prViewJSON は Collect のテスト用に、`gh pr view` の最小限の応答 JSON を作る。
func prViewJSON(title, updatedAt string) string {
	return fmt.Sprintf(
		`{"title":%q,"reviewDecision":"","updatedAt":%q,"commits":[],"reviews":[],"comments":[]}`,
		title, updatedAt)
}

// TestCollectSince は、閾値より古い PR が除外され、詳細取得も行われないことを確認する。
// 偽 Runner は未登録の呼び出しをエラーにするので、#1 の pr view / api を呼べばテストが失敗する。
func TestCollectSince(t *testing.T) {
	const viewFields = "title,reviewDecision,updatedAt,commits,reviews,comments"
	runner := fakeRunner(map[string]string{
		"pr list --repo o/r --author @me --state open --limit 200 --json number,updatedAt": `[
			{"number":1,"updatedAt":"2026-10-01T00:00:00Z"},
			{"number":2,"updatedAt":"2026-10-05T00:00:00Z"},
			{"number":3,"updatedAt":"2026-10-09T00:00:00Z"}
		]`,
		// #1(範囲外)の応答は意図的に登録しない。
		"pr view 2 --repo o/r --json " + viewFields: prViewJSON("境界ちょうど", "2026-10-05T00:00:00Z"),
		"api repos/o/r/pulls/2/comments --paginate": `[]`,
		"pr view 3 --repo o/r --json " + viewFields: prViewJSON("新しい", "2026-10-09T00:00:00Z"),
		"api repos/o/r/pulls/3/comments --paginate": `[]`,
	})

	since := mustTime(t, "2026-10-05T00:00:00Z")
	got, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me", since)
	if err != nil {
		t.Fatalf("Collect() が予期せず失敗しました: %v", err)
	}

	var nums []int
	for _, s := range got {
		nums = append(nums, s.Number)
	}
	// #1 は除外、#2 は境界ちょうどなので含まれる、#3 も含まれる。
	if want := []int{2, 3}; !reflect.DeepEqual(nums, want) {
		t.Errorf("PR 番号 = %v, want %v", nums, want)
	}
}

// TestCollectSinceNoMatch は、全件が範囲外のとき空の結果になる(エラーにならない)ことを確認する。
func TestCollectSinceNoMatch(t *testing.T) {
	runner := fakeRunner(map[string]string{
		"pr list --repo o/r --author @me --state open --limit 200 --json number,updatedAt": `[{"number":1,"updatedAt":"2026-01-01T00:00:00Z"}]`,
	})

	got, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me", mustTime(t, "2026-10-01T00:00:00Z"))
	if err != nil {
		t.Fatalf("Collect() が予期せず失敗しました: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("結果 = %d 件, want 0 件", len(got))
	}
}

// ---------------------------------------------------------------
// 取得件数の上限
// ---------------------------------------------------------------

// TestCollectPassesLimit は、gh pr list に --limit が明示されることを確認する。
// 省略すると gh の既定(30件)で打ち切られ、PR を黙って取りこぼすため。
func TestCollectPassesLimit(t *testing.T) {
	var listArgs string
	runner := func(args ...string) ([]byte, error) {
		if len(args) >= 2 && args[0] == "pr" && args[1] == "list" {
			listArgs = strings.Join(args, " ")
			return []byte(`[]`), nil
		}
		return nil, fmt.Errorf("想定外の gh 呼び出しです: %v", args)
	}

	if _, err := Collect(runner, "o/r", &Config{Author: "@me"}, "me", time.Time{}); err != nil {
		t.Fatalf("Collect() が予期せず失敗しました: %v", err)
	}
	if !strings.Contains(listArgs, "--limit 200") {
		t.Errorf("pr list に --limit 200 が渡されていません: %q", listArgs)
	}
}
