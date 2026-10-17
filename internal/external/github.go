package external

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// github.go — unduh repo GitHub publik sebagai ZIP (tanpa key).
// Branch default diambil dari GitHub API; ZIP via codeload. Repo privat
// tidak didukung (butuh token).

// GitRepo adalah repo + branch hasil parse input user.
type GitRepo struct {
	Owner  string
	Repo   string
	Branch string // "" = pakai default branch repo
}

// GitMeta adalah metadata repo untuk caption.
type GitMeta struct {
	FullName    string
	Description string
	Stars       int
	Branch      string
}

var gitSlugRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// ParseGitInput menerima "owner/repo[@branch]" atau URL github.com
// (termasuk berekor /tree/<branch>/..., /blob/..., .git) → GitRepo.
// Branch berisi "/" (mis. feature/x) hanya didukung via bentuk @branch.
func ParseGitInput(input string) (GitRepo, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return GitRepo{}, fmt.Errorf("input kosong")
	}
	// Bentuk URL: ambil path-nya, buang query/fragment.
	if strings.Contains(s, "://") || strings.HasPrefix(s, "github.com/") {
		if !strings.Contains(s, "://") {
			s = "https://" + s
		}
		path := s
		if i := strings.Index(path, "://"); i >= 0 {
			path = path[i+3:]
		}
		if i := strings.Index(path, "/"); i >= 0 {
			host, rest := path[:i], path[i+1:]
			if !strings.EqualFold(host, "github.com") && !strings.EqualFold(host, "www.github.com") {
				return GitRepo{}, fmt.Errorf("bukan URL github.com")
			}
			path = rest
		} else {
			return GitRepo{}, fmt.Errorf("URL tidak lengkap")
		}
		if i := strings.IndexAny(path, "?#"); i >= 0 {
			path = path[:i]
		}
		segs := strings.Split(strings.Trim(path, "/"), "/")
		if len(segs) < 2 {
			return GitRepo{}, fmt.Errorf("URL repo tidak lengkap (owner/repo)")
		}
		owner, repo := segs[0], strings.TrimSuffix(segs[1], ".git")
		branch := ""
		// /tree/<branch>/... atau /blob/<branch>/... → segmen pertama
		// setelahnya sebagai branch (branch berslash pakai bentuk @).
		if len(segs) > 3 && (segs[2] == "tree" || segs[2] == "blob") {
			branch = segs[3]
		}
		return checkGitSlug(owner, repo, branch)
	}
	// Bentuk shorthand: owner/repo[@branch].
	branch := ""
	if i := strings.LastIndex(s, "@"); i >= 0 {
		branch = strings.TrimSpace(s[i+1:])
		s = strings.TrimSpace(s[:i])
	}
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return GitRepo{}, fmt.Errorf("format: owner/repo[@branch] atau URL github.com")
	}
	return checkGitSlug(strings.TrimSpace(parts[0]), strings.TrimSuffix(strings.TrimSpace(parts[1]), ".git"), branch)
}

func checkGitSlug(owner, repo, branch string) (GitRepo, error) {
	if owner == "" || repo == "" {
		return GitRepo{}, fmt.Errorf("owner/repo tidak lengkap")
	}
	if !gitSlugRe.MatchString(owner) || !gitSlugRe.MatchString(repo) {
		return GitRepo{}, fmt.Errorf("owner/repo mengandung karakter invalid")
	}
	if branch != "" {
		for _, seg := range strings.Split(branch, "/") {
			if seg == "" || seg == "." || seg == ".." || !gitSlugRe.MatchString(seg) {
				return GitRepo{}, fmt.Errorf("branch mengandung karakter invalid")
			}
		}
	}
	return GitRepo{Owner: owner, Repo: repo, Branch: branch}, nil
}

type gitAPIShort struct {
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Stargazers    int    `json:"stargazers_count"`
	DefaultBranch string `json:"default_branch"`
}

// FetchGitRepo mengunduh ZIP repo (branch default bila kosong) + metadata.
// Gagal jelas untuk repo tak ada/privat (API 404) atau branch salah (404).
func FetchGitRepo(ctx context.Context, r GitRepo) ([]byte, GitMeta, error) {
	const engine = "github"
	meta := GitMeta{Branch: r.Branch}
	// Metadata selalu diambil (branch default + caption); 1 request.
	{
		var info gitAPIShort
		tctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		err := fetchJSON(tctx, engine, "https://api.github.com/repos/"+r.Owner+"/"+r.Repo, nil, &info)
		cancel()
		if err != nil {
			return nil, meta, fmt.Errorf("repo tidak ditemukan/privat: %v", err)
		}
		meta.FullName = firstNonEmpty(info.FullName, r.Owner+"/"+r.Repo)
		meta.Description = strings.TrimSpace(info.Description)
		meta.Stars = info.Stargazers
		if r.Branch == "" {
			meta.Branch = firstNonEmpty(info.DefaultBranch, "main")
		}
	}
	zipURL := "https://codeload.github.com/" + r.Owner + "/" + r.Repo + "/zip/refs/heads/" + meta.Branch
	data, err := DownloadBytes(ctx, zipURL)
	if err != nil {
		return nil, meta, fmt.Errorf("unduh ZIP gagal (branch %q salah?): %v", meta.Branch, err)
	}
	if len(data) < 4 || string(data[:2]) != "PK" {
		return nil, meta, fmt.Errorf("respon bukan ZIP (repo dipindah/dihapus?)")
	}
	return data, meta, nil
}
