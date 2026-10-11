package profiles

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strings"
)

const builderVersion = "1"

var ErrInvalid = errors.New("invalid profile")

var (
	//go:embed flake.lock
	flakeLock []byte

	attributePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_'-]*(\.[A-Za-z_][A-Za-z0-9_'-]*)*$`)
	refPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)
	required         = []string{"chezmoi", "git", "tmux"}
)

const (
	maxPackages  = 300
	maxModule    = 64 << 10
	maxAttribute = 100
)

type Dotfiles struct {
	Repo string `json:"repo"`
	Ref  string `json:"ref,omitempty"`
}

type Spec struct {
	Packages []string  `json:"packages,omitempty"`
	Module   string    `json:"module,omitempty"`
	Unfree   bool      `json:"unfree"`
	Dotfiles *Dotfiles `json:"dotfiles,omitempty"`
}

func (s Spec) Normalize() (Spec, error) {
	if s.Module != "" && len(s.Packages) > 0 {
		return Spec{}, fmt.Errorf("%w: use packages or a module, not both; a module can list its own packages", ErrInvalid)
	}
	if len(s.Module) > maxModule || strings.ContainsRune(s.Module, 0) {
		return Spec{}, fmt.Errorf("%w: module must be at most %d KiB of text", ErrInvalid, maxModule>>10)
	}
	if len(s.Packages) > maxPackages {
		return Spec{}, fmt.Errorf("%w: at most %d packages", ErrInvalid, maxPackages)
	}
	packages := []string{}
	for _, p := range s.Packages {
		p = strings.TrimSpace(p)
		if len(p) > maxAttribute || !attributePattern.MatchString(p) {
			return Spec{}, fmt.Errorf("%w: %q is not a nixpkgs attribute name", ErrInvalid, p)
		}
		if !slices.Contains(required, p) {
			packages = append(packages, p)
		}
	}
	slices.Sort(packages)
	s.Packages = slices.Compact(packages)
	if s.Dotfiles != nil {
		u, err := url.Parse(s.Dotfiles.Repo)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return Spec{}, fmt.Errorf("%w: dotfiles repository must be a public https URL without credentials", ErrInvalid)
		}
		if s.Dotfiles.Ref != "" && (!refPattern.MatchString(s.Dotfiles.Ref) || strings.Contains(s.Dotfiles.Ref, "..")) {
			return Spec{}, fmt.Errorf("%w: dotfiles ref %q is not a branch, tag, or commit", ErrInvalid, s.Dotfiles.Ref)
		}
		d := *s.Dotfiles
		s.Dotfiles = &d
	}
	if len(s.Packages) == 0 {
		s.Packages = nil
	}
	return s, nil
}

func BuildKey(ownerID string, s Spec) string {
	spec, _ := json.Marshal(s)
	sum := sha256.New()
	for _, part := range [][]byte{[]byte(builderVersion), []byte(ownerID), flakeLock, spec} {
		fmt.Fprintf(sum, "%d:", len(part))
		sum.Write(part)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

type pin struct {
	Owner string `json:"owner"`
	Repo  string `json:"repo"`
	Rev   string `json:"rev"`
}

func pins() (nixpkgs, homeManager pin) {
	var lock struct {
		Nodes map[string]struct {
			Locked pin `json:"locked"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(flakeLock, &lock); err != nil {
		panic(err)
	}
	return lock.Nodes["nixpkgs"].Locked, lock.Nodes["home-manager"].Locked
}

func Flake(s Spec) map[string]string {
	nixpkgs, homeManager := pins()
	packages := append(slices.Clone(required), s.Packages...)
	modules := "            ./user.nix\n"
	if s.Module == "" {
		modules = ""
	}
	flake := fmt.Sprintf(`{
  inputs = {
    nixpkgs.url = "github:%s/%s/%s";
    home-manager = {
      url = "github:%s/%s/%s";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { nixpkgs, home-manager, ... }:
    let
      pkgs = import nixpkgs {
        system = "x86_64-linux";
        config.allowUnfree = %t;
      };
    in
    {
      homeConfigurations.dev = home-manager.lib.homeManagerConfiguration {
        inherit pkgs;
        modules = [
          {
            home.username = "dev";
            home.homeDirectory = "/home/dev";
            home.stateVersion = "26.05";
            home.packages = with pkgs; [ %s ];
          }
%s        ];
      };
    };
}
`, nixpkgs.Owner, nixpkgs.Repo, nixpkgs.Rev, homeManager.Owner, homeManager.Repo, homeManager.Rev, s.Unfree, strings.Join(packages, " "), modules)
	files := map[string]string{"flake.nix": flake, "flake.lock": string(flakeLock)}
	if s.Module != "" {
		files["user.nix"] = s.Module
	}
	return files
}
