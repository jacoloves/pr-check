// pr-check: 設定ファイルで指定したリポジトリについて、PR作成者のオープンPRの
// レビュー状況と「要対応」件数を表示する。
package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
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
	// 未知のキーをエラーにして、タイプミス(repositories 等)を検知する。
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
// 設定ファイルの探索
// ---------------------------------------------------------------

// ConfigCandidates は -config 未指定時に探索するパスを、優先順に返す。
//
//  1. ./config.yaml                              (カレントディレクトリ)
//  2. $XDG_CONFIG_HOME/pr-check/config.yaml      (XDG_CONFIG_HOME が設定されている場合のみ)
//  3. ~/.config/pr-check/config.yaml             (ホームが判明している場合のみ)
//
// 環境変数とホームディレクトリは引数で受け取る。実環境に依存せずテストできるようにするため。
func ConfigCandidates(getenv func(string) string, homeDir string) []string {
	candidates := []string{"config.yaml"}

	// 同じパスが2回入らないようにする(例: XDG_CONFIG_HOME=~/.config のとき)。
	add := func(p string) {
		for _, c := range candidates {
			if c == p {
				return
			}
		}
		candidates = append(candidates, p)
	}

	if xdg := getenv("XDG_CONFIG_HOME"); xdg != "" {
		add(filepath.Join(xdg, "pr-check", "config.yaml"))
	}
	if homeDir != "" {
		add(filepath.Join(homeDir, ".config", "pr-check", "config.yaml"))
	}
	return candidates
}

// ResolveConfigPath は使用する設定ファイルのパスを決める。
//
//   - explicit が空でない場合: そのパスだけを確認する。存在しなくても他の候補には
//     フォールバックしない(タイプミスに気づけなくなるため)。
//   - explicit が空の場合: candidates を順に確認し、最初に存在するものを返す。
//
// exists はファイルの存在確認関数。テストでは偽物に差し替える。
func ResolveConfigPath(explicit string, candidates []string, exists func(string) bool) (string, error) {
	if explicit != "" {
		if !exists(explicit) {
			return "", fmt.Errorf(
				"-config で指定された設定ファイル %q が見つかりません。パスを確認してください(指定時は他の場所を探索しません)",
				explicit)
		}
		return explicit, nil
	}

	for _, c := range candidates {
		if exists(c) {
			return c, nil
		}
	}
	return "", fmt.Errorf(
		"設定ファイルが見つかりません。次の場所を探しました:\n  %s\n"+
			"config.example.yaml をコピーして上記のいずれかに配置するか、-config でパスを指定してください",
		strings.Join(candidates, "\n "))
}

// fileExists は path が通常のファイル(ディレクトリでない)として存在するかを返す。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
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
// 色付け
// ---------------------------------------------------------------

// Painter は文字列に ANSI の装飾を付ける。enabled が false のときは何も付けない
// (全メソッドが恒等変換になる)ので、呼び出し側は色の有無を意識しなくてよい。
type Painter struct {
	enabled bool
}

// NewPainter は色の有効/無効を指定して Painter を作る。
func NewPainter(enabled bool) Painter {
	return Painter{enabled: enabled}
}

// wrap は SGR コード code を s の前に付け、後ろでリセットする。
func (p Painter) wrap(code, s string) string {
	if !p.enabled {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func (p Painter) Bold(s string) string       { return p.wrap("1", s) }
func (p Painter) Faint(s string) string      { return p.wrap("2", s) }
func (p Painter) Red(s string) string        { return p.wrap("31", s) }
func (p Painter) Green(s string) string      { return p.wrap("32", s) }
func (p Painter) Yellow(s string) string     { return p.wrap("33", s) }
func (p Painter) BoldYellow(s string) string { return p.wrap("1;33", s) }

// ShouldColor は --color の mode ("auto" / "always" / "never") から、
// 実際に色を付けるかを決める。環境変数と端末かどうかは引数で受け取る
// (実環境に依存せずテストするため)。
func ShouldColor(mode string, getenv func(string) string, isTTY bool) (bool, error) {
	switch mode {
	case "always":
		return true, nil
	case "never":
		return false, nil
	case "auto":
		// NO_COLOR は「空でなければ無効」という慣習(https://no-color.org)。
		if getenv("NO_COLOR") != "" {
			return false, nil
		}
		if getenv("TERM") == "dumb" {
			return false, nil
		}
		return isTTY, nil
	default:
		return false, fmt.Errorf("--color の値 %q は指定できません。auto, always, never のいずれかを指定してください", mode)
	}
}

// isTerminal は f が端末(文字デバイス)かどうかを返す。
// パイプやファイルへのリダイレクト時は false になる。
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// ---------------------------------------------------------------
// 描画
// ---------------------------------------------------------------

// columnGap は列と列の間に空ける空白の数。従来の tabwriter の padding=2 と同じ。
const columnGap = 2

// cell は表の1マス。text は素の文字列、style は表示時に text へ掛ける装飾。
// 列幅は style を掛ける前の text で計算する(ANSI で列がずれないようにするため)。
type cell struct {
	text  string
	style func(string) string
}

// cellWidth は s を端末に表示したときの桁数を返す。全角文字は2桁、半角は1桁。
func cellWidth(s string) int { return runewidth.StringWidth(s) }

// plain は装飾を付けない style。
func plain(s string) string { return s }

// headerCells は表のヘッダ行のセルを返す。ヘッダは全て太字。
func headerCells(p Painter) []cell {
	names := []string{"リポジトリ", "PR", "タイトル", "レビュー", "要対応", "最終更新"}
	cells := make([]cell, len(names))
	for i, n := range names {
		cells[i] = cell{n, p.Bold}
	}
	return cells
}

// decisionStyle はレビュー判定に応じた装飾を返す。未知の値は装飾なし。
func decisionStyle(p Painter, decision string) func(string) string {
	switch decision {
	case "APPROVED":
		return p.Green
	case "CHANGES_REQUESTED":
		return p.Red
	case "REVIEW_REQUIRED":
		return p.Yellow
	case "NONE":
		return p.Faint
	default:
		return plain
	}
}

// pendingStyle は要対応件数に応じた装飾を返す。1件以上は目立たせ、0件は控えめにする。
func pendingStyle(p Painter, n int) func(string) string {
	if n > 0 {
		return p.BoldYellow
	}
	return p.Faint
}

// summaryCells は1PR分の Summary を、表の1行分のセルに変換する。
func summaryCells(s Summary, p Painter) []cell {
	return []cell{
		{s.Repo, plain},
		{strconv.Itoa(s.Number), plain},
		{s.Title, plain},
		{s.ReviewDecision, decisionStyle(p, s.ReviewDecision)},
		{strconv.Itoa(s.Pending), pendingStyle(p, s.Pending)},
		{s.UpdatedAt.Local().Format("2006-01-02 15:04"), plain},
	}
}

// Render は summaries をヘッダ付きの表として w に書き出す。
//
// 各列の幅は「その列で一番長いセルの素の文字数」に揃え、列間に columnGap 個の空白を入れる。
// 色は文字の部分だけに付け、パディングの空白には付けない。
// 最終列には空白を詰めない(行末に余分な空白を残さない)。
// 幅は端末での表示幅で数える(全角文字は2桁、半角は1桁)。
func Render(w io.Writer, summaries []Summary, p Painter) error {
	rows := make([][]cell, 0, len(summaries)+1)
	rows = append(rows, headerCells(p))
	for _, s := range summaries {
		rows = append(rows, summaryCells(s, p))
	}

	// 列ごとの最大幅を、装飾前の text で求める。
	widths := make([]int, len(rows[0]))
	for _, row := range rows {
		for i, c := range row {
			if n := cellWidth(c.text); n > widths[i] {
				widths[i] = n
			}
		}
	}

	for _, row := range rows {
		var b strings.Builder
		for i, c := range row {
			b.WriteString(c.style(c.text)) // 色は文字だけに付ける
			if i == len(row)-1 {
				break // 最終列は空白で埋めない
			}
			pad := widths[i] - cellWidth(c.text) + columnGap
			b.WriteString(strings.Repeat(" ", pad)) // 空白は素の文字数で計算
		}
		b.WriteByte('\n')

		if _, err := io.WriteString(w, b.String()); err != nil {
			return fmt.Errorf(
				"表の書き出しに失敗しました: %w (出力先がクローズされていないか、ディスク容量やパイプの状態を確認してください)",
				err,
			)
		}
	}
	return nil
}

// ---------------------------------------------------------------
// ソート
// ---------------------------------------------------------------

// SortKey は --sort で指定できる並べ替えの基準。
type SortKey string

const (
	// SortNone は並べ替えない(取得順=設定ファイル順のまま)。
	SortNone    SortKey = ""
	SortPending SortKey = "pending" // 要対応が多い順
	SortUpdated SortKey = "updated" // 更新が新しい順
	SortReview  SortKey = "review"  // レビューの緊急度順
	SortRepo    SortKey = "repo"    // リポジトリ名の昇順
	SortNumber  SortKey = "number"  // PR番号の昇順
)

// sortKeys は指定可能なキーの一覧。エラーメッセージの表示にも使う。
var sortKeys = []SortKey{SortPending, SortUpdated, SortReview, SortRepo, SortNumber}

// ParseSortKey は --sort の文字列を検証して SortKey に変換する。
// 空文字は「ソートしない」を表す。
func ParseSortKey(s string) (SortKey, error) {
	if s == "" {
		return SortNone, nil
	}
	for _, k := range sortKeys {
		if SortKey(s) == k {
			return k, nil
		}
	}

	names := make([]string, len(sortKeys))
	for i, k := range sortKeys {
		names[i] = string(k)
	}
	return SortNone, fmt.Errorf(
		"--sort の値 %q は指定できません。次のいずれかを指定してください: %s",
		s, strings.Join(names, ", "),
	)
}

// reviewRank はレビュー判定の緊急度。小さいほど先に表示する。
// 「自分が動くべきもの」から順に並べる意図。
var reviewRank = map[string]int{
	"CHANGES_REQUESTED": 0,
	"REVIEW_REQUIRED":   1,
	"NONE":              2,
	"APPROVED":          3,
}

// reviewOrder は判定の緊急度を返す。未知の値は最後尾に回す
// (gh 側で値が増えても、落とさず末尾に並べるため)。
func reviewOrder(decision string) int {
	if r, ok := reviewRank[decision]; ok {
		return r
	}
	return len(reviewRank)
}

// comparatorFor はキーに対応する比較関数を返す。戻り値は cmp.Compare と同じ規約
// (a が先なら負、b が先なら正、同順位なら 0)。
// 同順位のときは cmp.Or で次の基準に進む。
func comparatorFor(key SortKey) func(a, b Summary) int {
	switch key {
	case SortPending:
		return func(a, b Summary) int {
			return cmp.Or(
				cmp.Compare(b.Pending, a.Pending), // 多い順(引数を入れ替えて降順)
				b.UpdatedAt.Compare(a.UpdatedAt),  // 新しい順
			)
		}
	case SortUpdated:
		return func(a, b Summary) int {
			return cmp.Or(
				b.UpdatedAt.Compare(a.UpdatedAt), // 新しい順
				cmp.Compare(a.Repo, b.Repo),
				cmp.Compare(a.Number, b.Number),
			)
		}
	case SortReview:
		return func(a, b Summary) int {
			return cmp.Or(
				cmp.Compare(reviewOrder(a.ReviewDecision), reviewOrder(b.ReviewDecision)),
				cmp.Compare(b.Pending, a.Pending), // 多い順
			)
		}
	case SortRepo:
		return func(a, b Summary) int {
			return cmp.Or(
				cmp.Compare(a.Repo, b.Repo),
				cmp.Compare(a.Number, b.Number),
			)
		}
	case SortNumber:
		return func(a, b Summary) int {
			return cmp.Or(
				cmp.Compare(a.Number, b.Number),
				cmp.Compare(a.Repo, b.Repo),
			)
		}
	default:
		return nil
	}
}

// SortSummaries は s を key の基準で並べ替える(引数のスライスを直接変更する)。
// key が SortNone の場合は何もしない。reverse が true なら比較結果を反転する。
// 安定ソートなので、完全に同順位の要素は元の順序を保つ。
func SortSummaries(s []Summary, key SortKey, reverse bool) {
	compare := comparatorFor(key)
	if compare == nil {
		return
	}
	if reverse {
		base := compare
		compare = func(a, b Summary) int { return -base(a, b) }
	}
	slices.SortStableFunc(s, compare)
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
	// 既定値を "" にして「明示指定」と「未指定(探索する)」を区別する。
	configFlag := flag.String("config", "", "設定ファイル(YAML)のパス。省略時は ./config.yaml → $XDG_CONFIG_HOME/pr-check/config.yaml → ~/.config/pr-check/config.yaml の順に探索")
	sortFlag := flag.String("sort", "", "並べ替えの基準: pending(要対応が多い順) / updated(更新が新しい順) / review(レビュー緊急度順) / repo / number。省略時は設定ファイル順")
	reverseFlag := flag.Bool("reverse", false, "並び順を反転する(--sort と併用)")
	colorFlag := flag.String("color", "auto", "色付け: auto(端末のときだけ) / always / never。auto では NO_COLOR 環境変数も尊重する")
	flag.Parse()

	// 引数の誤りは、時間のかかる gh の呼び出しの前に検出する。
	sortKey, err := ParseSortKey(*sortFlag)
	if err != nil {
		return err
	}
	if *reverseFlag && sortKey == SortNone {
		return fmt.Errorf("--reverse は --sort と一緒に指定してください(例: --sort updated --reverse)")
	}
	useColor, err := ShouldColor(*colorFlag, os.Getenv, isTerminal(os.Stdout))
	if err != nil {
		return err
	}

	// ホームが取得できなくても致命的ではない(その場合はホーム配下の候補を除外するだけ)。
	home, _ := os.UserHomeDir()
	configPath, err := ResolveConfigPath(*configFlag, ConfigCandidates(os.Getenv, home), fileExists)
	if err != nil {
		return err
	}

	// どの設定ファイルが使われたかを標準エラー出力に表示する。
	// 標準出力(PR一覧)を汚さないので、パイプやリダイレクトに影響しない。
	fmt.Fprintf(os.Stderr, "設定ファイル: %s\n", configPath)

	cfg, err := LoadConfig(configPath)
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

	// 全リポジトリの結果を集約してから描画する。
	// (後続のソートは「全件が揃っていること」が前提になるため)
	var all []Summary
	for _, repo := range cfg.Repositories {
		summaries, err := Collect(ghRun, repo, cfg, self)
		if err != nil {
			return err
		}
		all = append(all, summaries...)
	}
	SortSummaries(all, sortKey, *reverseFlag)
	return Render(os.Stdout, all, NewPainter(useColor))
}
