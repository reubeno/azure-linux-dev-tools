// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tmt

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/microsoft/azure-linux-dev-tools/internal/global/opctx"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileperms"
	"github.com/microsoft/azure-linux-dev-tools/internal/utils/fileutils"
)

const testcloudPluginTemplate = `import tmt.steps.provision.testcloud as testcloud
from tmt.guest import GuestSsh
from tmt.utils import Command

# Guest access uses the generated key. Do not enable password SSH on an image
# whose own policy disables it.
if testcloud.USER_DATA.count("ssh_pwauth: true") != 1:
	raise RuntimeError("Unsupported TMT cloud-init SSH template")
testcloud.USER_DATA = testcloud.USER_DATA.replace("ssh_pwauth: true", "ssh_pwauth: false")

testcloud.TESTCLOUD_WORKAROUNDS.extend(%s)

# Finish tasks pull their artifacts immediately. A later finish task cannot
# repair root-owned output in time, so fix permissions before each pull.
_azldev_original_pull = GuestSsh.pull

def _azldev_pull(self, source=None, destination=None, options=None):
	path = source or self.plan_workdir
	if self.user != "root":
		self.execute(Command("sudo", "chmod", "-R", "a+rX", path), silent=True)
	return _azldev_original_pull(self, source=source, destination=destination, options=options)

GuestSsh.pull = _azldev_pull
`

// The pinned TMT release uses a 30-second default poll interval when starting
// each SSH master, even when authentication completes immediately. Preserve its
// deadline and backoff policy but check readiness at most one second apart.
const sshWaitingPlugin = `
import tmt.guest as _azldev_guest

_azldev_original_waiting = _azldev_guest.default_ssh_master_start_waiting

def _azldev_ssh_waiting():
    waiting = _azldev_original_waiting()
    waiting.tick = min(waiting.tick, 1.0)
    return waiting

_azldev_guest.default_ssh_master_start_waiting = _azldev_ssh_waiting
`

func writeTestcloudPlugin(ctx opctx.Ctx, workDir string, extraRuncmds ...string) (string, error) {
	workarounds := []string{
		"id -u fedora >/dev/null 2>&1 || useradd --create-home --groups wheel fedora",
		// Testcloud 0.12 waits for its guest HTTP service on TCP 10022 before
		// attempting SSH. Open only these temporary guest rules, not the firewall.
		"if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then " +
			"firewall-cmd --add-service=ssh && firewall-cmd --add-port=10022/tcp; fi",
	}
	workarounds = append(workarounds, extraRuncmds...)

	encoded, err := json.Marshal(workarounds)
	if err != nil {
		return "", fmt.Errorf("encode testcloud workarounds:\n%w", err)
	}

	return writePlugin(ctx, workDir, "azldev_testcloud.py",
		fmt.Sprintf(testcloudPluginTemplate, encoded)+sshWaitingPlugin)
}

func writeSSHPlugin(ctx opctx.Ctx, workDir string) (string, error) {
	return writePlugin(ctx, workDir, "azldev_ssh.py", sshWaitingPlugin)
}

func writePlugin(ctx opctx.Ctx, workDir, name, body string) (string, error) {
	pluginDir := filepath.Join(workDir, "tmt-plugins")
	if err := fileutils.MkdirAll(ctx.FS(), pluginDir); err != nil {
		return "", fmt.Errorf("create testcloud plugin directory:\n%w", err)
	}

	pluginPath := filepath.Join(pluginDir, name)

	if err := fileutils.WriteFile(ctx.FS(), pluginPath, []byte(body), fileperms.PrivateFile); err != nil {
		return "", fmt.Errorf("write testcloud plugin:\n%w", err)
	}

	return pluginDir, nil
}
