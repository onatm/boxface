package lifecycle

import "context"

type Credentials map[string]map[string]string

type DiskSpec struct {
	DiskID        string
	EnvironmentID string
	SizeGiB       int
}

type VMSpec struct {
	EnvironmentID  string
	DiskID         string
	ImageID        string
	VCPUs          int
	MemoryMiB      int
	AuthorizedKeys []string
	ProfileID      string
}

type BuildSpec struct {
	BuildID      string
	OwnerID      string
	Files        map[string]string
	DotfilesRepo string
	DotfilesRef  string
	VCPUs        int
	MemoryMiB    int
	TimeoutSec   int
}

type BuildStatus struct {
	State          string `json:"state"`
	HomePath       string `json:"home_path,omitempty"`
	ClosureMiB     int    `json:"closure_mib,omitempty"`
	DotfilesCommit string `json:"dotfiles_commit,omitempty"`
	Error          string `json:"error,omitempty"`
	LogTail        string `json:"log_tail,omitempty"`
}

type Worker interface {
	CreateDisk(ctx context.Context, spec DiskSpec) error
	RemoveDisk(ctx context.Context, diskID string) error
	CreateNetwork(ctx context.Context, environmentID string) error
	RemoveNetwork(ctx context.Context, environmentID string) error
	StartVM(ctx context.Context, spec VMSpec) error
	StopVM(ctx context.Context, environmentID string) error
	WaitReady(ctx context.Context, environmentID string) error
}
