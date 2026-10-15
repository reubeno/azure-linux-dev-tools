// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package lisa_test

import "fmt"

type lisaExitError int

func (e lisaExitError) Error() string { return fmt.Sprintf("native exit %d", e) }
func (e lisaExitError) ExitCode() int { return int(e) }
