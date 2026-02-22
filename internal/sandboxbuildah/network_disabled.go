package sandboxbuildah

import (
	"errors"

	nettypes "go.podman.io/common/libnetwork/types"
)

var errSandboxNetworkDisabled = errors.New("sandbox networking is disabled")

// disabledNetwork is a minimal libnetwork implementation used when the sandbox
// explicitly disables networking. It lets Buildah create/open builders without
// touching netavark/CNI for command execution paths that do not need networking.
type disabledNetwork struct{}

func newDisabledNetwork() nettypes.ContainerNetwork {
	return disabledNetwork{}
}

func (disabledNetwork) NetworkCreate(
	nettypes.Network,
	*nettypes.NetworkCreateOptions,
) (nettypes.Network, error) {
	return nettypes.Network{}, errSandboxNetworkDisabled
}

func (disabledNetwork) NetworkUpdate(string, nettypes.NetworkUpdateOptions) error {
	return errSandboxNetworkDisabled
}

func (disabledNetwork) NetworkRemove(string) error {
	return errSandboxNetworkDisabled
}

func (disabledNetwork) NetworkList(...nettypes.FilterFunc) ([]nettypes.Network, error) {
	return nil, errSandboxNetworkDisabled
}

func (disabledNetwork) NetworkInspect(string) (nettypes.Network, error) {
	return nettypes.Network{}, errSandboxNetworkDisabled
}

func (disabledNetwork) Setup(
	string,
	nettypes.SetupOptions,
) (map[string]nettypes.StatusBlock, error) {
	return map[string]nettypes.StatusBlock{}, nil
}

func (disabledNetwork) Teardown(string, nettypes.TeardownOptions) error {
	return nil
}

func (disabledNetwork) RunInRootlessNetns(toRun func() error) error {
	if toRun == nil {
		return nil
	}
	return toRun()
}

func (disabledNetwork) RootlessNetnsInfo() (*nettypes.RootlessNetnsInfo, error) {
	return &nettypes.RootlessNetnsInfo{}, nil
}

func (disabledNetwork) Drivers() []string {
	return []string{"none"}
}

func (disabledNetwork) DefaultNetworkName() string {
	return "none"
}

func (disabledNetwork) NetworkInfo() nettypes.NetworkInfo {
	return nettypes.NetworkInfo{}
}
