//go:build !windows

package gui

import (
	"context"
	"errors"
	"remoteops/internal/i18n"
	"remoteops/internal/worker/client"
)

func Run(ctx context.Context, app *client.App) error {
	language := i18n.Chinese
	if app != nil {
		language = app.Language()
	}
	return errors.New(i18n.Text(language, "worker.gui.platform"))
}
