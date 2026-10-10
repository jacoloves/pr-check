// pr-check: 設定ファイルで指定したリポジトリについて、PR作成者のオープンPRの
// レビュー状況と「要対応」件数を表示する。
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v3"
)

// ---------------------------------------------------------------
// 設定ファイル
// ---------------------------------------------------------------

// Config は YAML 設定ファイルの内容。
type Config struct {
	// Author は PR 作成者。"@me" なら gh にログイン中のユーザー。
	Author string `yaml:"author"`
	// Repositories は確認対象のリポジトリ("owner/repo" 形式)。
	Repositories []string `yaml:"repositories"`
	// ExcludeUsers は「要対応」に数えないユーザーのログイン名(botなど)。
	ExcludeUsers []string `yaml:"exclude_users"`
}

// ParseConfig は YAML のバイト列を Config に変換し、内容を検証する。
// ファイル I/O を含まないので、テストしやすい。
func ParseConfig(path string) (*Config, error) {
	var cfg Config

	dec := yaml.NewDecoder(strings.NewReader(path))
	// 未知のキーをエラーにして、タイプミス(repositorys 等)を検知する。
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("設定ファイルが空です。config.example.yaml をコピーして repositories を設定してください")
		}
		return nil, fmt.Errorf(
			"設定ファイルのYAMLパースに失敗しました: %w (インデントやキー名のタイプミスを確認してください)", err)
	}

	// 省略時は gh にログイン中のユーザーとみなす。
	if cfg.Author == "" {
		cfg.Author = "@me"
	}

	if len(cfg.Repositories) == 0 {
		return nil, fmt.Errorf(
			"設定ファイルの repositories が空です。確認対象を \"owner/repo\" 形式で1件以上指定してください")
	}
	for _, r := range cfg.Repositories {
		parts := strings.Split(r, "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf(
				"repositories の値 %q が不正です。\"owner/repo\" 形式で指定してください(例: stanby-inc/rpa-projects)", r)
		}
	}
	return &cfg, nil
}

// LoadConfig は設定ファイルを読み込んで Config を返す。
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf(
			"設定ファイル %q を読み込めません: %w (パスが正しいか、config.example.yaml から作成済みか確認してください)",
			path, err)
	}
	cfg, err := ParseConfig(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// ---------------------------------------------------------------
// 要対応の判定
// ---------------------------------------------------------------

// Event は「誰が・いつ」行動したかを表す。
// コミット/レビュー/コメントの取得元の違いを吸収する共通表現。
type Event struct {
	At  time.Time
	Who string
}

// CountPending は、自分の最終アクションより後に発生した他者の
// イベント数(=要対応件数)を返す。副作用のない純粋関数なのでテストしやすい。
func CountPending(events []Event, me string) int {
	var last time.Time // ゼロ値 = 自分のアクションが無い場合は全件が対象
	for _, e := range events {
		if e.Who == me && e.At.After(last) {
			last = e.At
		}
	}
	n := 0
	for _, e := range events {
		if e.Who != me && e.At.After(last) {
			n++
		}
	}
	return n
}

// ---------------------------------------------------------------
// gh の実行
// ---------------------------------------------------------------

// Runner は gh コマンドを実行する関数。テストでは偽物に差し替える。
type Runner func(args ...string) ([]byte, error)

// ghRun は実際に gh を呼ぶ Runner。失敗時は原因と対処を含むエラーにする。
func ghRun(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf(
			"gh %s の実行に失敗しました: %w (stderr: %s)。"+
				"gh がインストールされ `gh auth status` が成功するか、対象リポジトリへの権限があるか確認してください",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// --- gh の JSON 出力に対応する構造体 ---

type author struct {
	Login string `yaml:"login"`
}

// prView は `gh pr view --json ...` の出力。
type prView struct {
	Title          string    `json:"title"`
	ReviewDecision string    `json:"reviewDecision"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Commits        []struct {
		CommittedDate time.Time `json:"committedDate"`
	} `json:"commits"`
	Reviews []struct {
		Author      author    `json:"author"`
		SubmittedAt time.Time `json:"submittedAt"`
	} `json:"reviews"`
	Comments []struct {
		Author    author    `json:"author"`
		CreatedAt time.Time `json:"createdAt"`
	} `json:"comments"`
}

// inlineComment は REST API のコード行コメント。
// キー名が GraphQL 系(createdAt / author)と異なる(created_at / user)点に注意。
type inlineComment struct {
	User      author    `json:"user"`
	CreatedAt time.Time `json:"created_at"`
}

// decodeAll は `gh api --paginate` が出す「配列が連結された出力」を全て読み込む。
func decodeAll[T any](data []byte) ([]T, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var all []T
	for {
		var page []T
		if err := dec.Decode(&page); err == io.EOF {
			return all, nil
		} else if err != nil {
			return nil, fmt.Errorf("JSON のパースに失敗しました: %w (gh の出力形式が変わった可能性があります)", err)
		}
		all = append(all, page...)
	}
}

// ---------------------------------------------------------------
// 収集
// ---------------------------------------------------------------

// Summary は1PR分の表示内容。
type Summary struct {
	Repo           string
	Number         int
	Title          string
	ReviewDecision string
	Pending        int
	UpdatedAt      time.Time
}

// Collect は1つのリポジトリについて、cfg.Author のオープンPRを列挙し、
// それぞれの要約を作る。self は「自分の行動」とみなす作成者のログイン名
// ("@me" 解決済みの値)。
func Collect(run Runner, repo string, cfg *Config, self string) ([]Summary, error) {
	// 除外ユーザーを高速に引けるよう集合にする。
	exclude := make(map[string]bool, len(cfg.ExcludeUsers))
	for _, u := range cfg.ExcludeUsers {
		exclude[u] = true
	}

	out, err := run("pr", "list", "--repo", repo, "--author", cfg.Author, "--state", "open", "--json", "number")
	if err != nil {
		return nil, err
	}
	var list []struct {
		Number int `json:"number"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, fmt.Errorf("%s のPR一覧のパースに失敗しました: %w", repo, err)
	}

	var result []Summary
	for _, p := range list {
		num := fmt.Sprint(p.Number)

		raw, err := run("pr", "view", num, "--repo", repo, "--json", "title,reviewDecision,updatedAt,commits,reviews,comments")
		if err != nil {
			return nil, err
		}
		var v prView
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s の PR #%s の詳細のパースに失敗しました: %w", repo, num, err)
		}

		rawInline, err := run("api", fmt.Sprintf("repos/%s/pulls/%s/comments", repo, num), "--paginate")
		if err != nil {
			return nil, err
		}
		inline, err := decodeAll[inlineComment](rawInline)
		if err != nil {
			return nil, err
		}

		// 取得元ごとの違いを Event に正規化する。
		// 他者のうち exclude_users に含まれるユーザーの行動は追加しない。
		var events []Event
		add := func(at time.Time, who string) {
			if who != self && exclude[who] {
				return
			}
			events = append(events, Event{at, who})
		}
		// コミットは PR 作成者(=self)の行動として扱う。
		for _, c := range v.Commits {
			add(c.CommittedDate, self)
		}
		for _, r := range v.Reviews {
			add(r.SubmittedAt, r.Author.Login)
		}
		for _, c := range v.Comments {
			add(c.CreatedAt, c.Author.Login)
		}
		for _, c := range inline {
			add(c.CreatedAt, c.User.Login)
		}

		decision := v.ReviewDecision
		if decision == "" {
			decision = "NONE"
		}
		result = append(result, Summary{
			Repo:           repo,
			Number:         p.Number,
			Title:          v.Title,
			ReviewDecision: decision,
			Pending:        CountPending(events, self),
			UpdatedAt:      v.UpdatedAt,
		})
	}
	return result, nil
}

// ---------------------------------------------------------------
// エントリポイント
// ---------------------------------------------------------------

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "エラー:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.yaml", "設定ファイル(YAML)のパス")
	flag.Parse()

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		return err
	}

	// "@me" は gh にログイン中のユーザー名に解決する。
	// 「自分の行動」の判定にはログイン名そのものが必要なため。
	self := cfg.Author
	if self == "@me" {
		out, err := ghRun("api", "user", "--jq", ".login")
		if err != nil {
			return err
		}
		self = strings.TrimSpace(string(out))
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "リポジトリ\tPR\tタイトル\tレビュー\t要対応\t最終更新")
	for _, repo := range cfg.Repositories {
		summaries, err := Collect(ghRun, repo, cfg, self)
		if err != nil {
			return err
		}
		for _, s := range summaries {
			fmt.Fprintf(w, "%s\t%d\t%s\t%s\t%d\t%s\n",
				s.Repo, s.Number, s.Title, s.ReviewDecision, s.Pending,
				s.UpdatedAt.Local().Format("2006-01-02 15:04"))
		}
	}
	return w.Flush()
}
