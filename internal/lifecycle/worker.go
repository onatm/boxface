package lifecycle

import "context"

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
