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

func writeTestcloudPlugin(ctx opctx.Ctx, workDir string) (string, error) {
	workarounds := []string{
		"id -u fedora >/dev/null 2>&1 || useradd --create-home --groups wheel fedora",
		"firewall-cmd --add-service=ssh || :",
	}

	encoded, err := json.Marshal(workarounds)
	if err != nil {
		return "", fmt.Errorf("encode testcloud workarounds:\n%w", err)
	}

	pluginDir := filepath.Join(workDir, "tmt-plugins")
	if err := fileutils.MkdirAll(ctx.FS(), pluginDir); err != nil {
		return "", fmt.Errorf("create testcloud plugin directory:\n%w", err)
	}

	plugin := fmt.Sprintf(testcloudPluginTemplate, encoded)
	pluginPath := filepath.Join(pluginDir, "azldev_testcloud.py")

	if err := fileutils.WriteFile(ctx.FS(), pluginPath, []byte(plugin), fileperms.PrivateFile); err != nil {
		return "", fmt.Errorf("write testcloud plugin:\n%w", err)
	}

	return pluginDir, nil
}
