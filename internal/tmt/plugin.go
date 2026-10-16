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

// These shims touch only VM provisioning. Keep them until upstream provides
// structured cloud-config overrides and privileged pull (as it does for push).
const testcloudPluginTemplate = `import io
import sys
from pathlib import Path

import tmt.steps.provision.testcloud as provision
from tmt.guest import DEFAULT_PULL_OPTIONS
from ruamel.yaml import YAML

from testcloud.instance import Instance

provision.TESTCLOUD_WORKAROUNDS.extend(%s)

_azldev_original_user_data = Instance._create_user_data

def _azldev_user_data(self, password, user_data_tpl=None, overwrite=False):
    _azldev_original_user_data(self, password, user_data_tpl=user_data_tpl, overwrite=overwrite)
    # testcloud has expanded users, keys, passwords and readiness commands.
    # Its prepare() builds the seed ISO only after this method returns.
    path = Path(self.path) / "meta" / "user-data"
    text = path.read_text()
    if not text.startswith("#cloud-config\n"):
        raise RuntimeError("Unsupported testcloud user-data format")
    yaml = YAML(typ="safe")
    yaml.version = (1, 1)
    # Avoid folding long shell commands: folding can change quoted whitespace.
    yaml.width = sys.maxsize
    config = yaml.load(text)
    if not isinstance(config, dict):
        raise RuntimeError("Expected a cloud-config mapping")
    config["ssh_pwauth"] = False
    rendered = io.StringIO()
    yaml.dump(config, rendered)
    path.write_text("#cloud-config\n" + rendered.getvalue())

Instance._create_user_data = _azldev_user_data

class _AzldevPullOptions:
    def __init__(self, original):
        self.original = original

    def to_rsync(self):
        return [*self.original.to_rsync(), "--rsync-path", "sudo -n rsync"]

    def __getattr__(self, name):
        return getattr(self.original, name)

_azldev_original_pull = provision.GuestTestcloud.pull

def _azldev_pull(self, source=None, destination=None, options=None):
    if self.user != "root" and self.become and not self.is_dry_run:
        options = _AzldevPullOptions(options if options is not None else DEFAULT_PULL_OPTIONS)
    return _azldev_original_pull(self, source=source, destination=destination, options=options)

provision.GuestTestcloud.pull = _azldev_pull
`

func writeTestcloudPlugin(ctx opctx.Ctx, workDir string, runcmds ...string) (string, error) {
	if runcmds == nil {
		runcmds = []string{}
	}

	encoded, err := json.Marshal(runcmds)
	if err != nil {
		return "", fmt.Errorf("encode testcloud workarounds:\n%w", err)
	}

	return writePlugin(ctx, workDir, "azldev_testcloud.py",
		fmt.Sprintf(testcloudPluginTemplate, encoded))
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
