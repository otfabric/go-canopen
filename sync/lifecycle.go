// SPDX-License-Identifier: MIT

package syncobj

import (
	"io"
	"log/slog"
	"time"
)

// sendTimeout bounds every frame a service sends on its own.
const sendTimeout = time.Second

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))
