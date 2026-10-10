package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
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
