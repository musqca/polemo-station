package cmd

import (
	"fmt"
	"io"
	"os"

	"github.com/mdp/qrterminal/v4"
	"github.com/musqca/polemo-station/internal/station"
)

func printBootstrapAuthorization(writer io.Writer, authorization station.BootstrapAuthorization) {
	if file, ok := writer.(*os.File); ok && isTerminal(file) {
		qrterminal.GenerateWithConfig(authorization.URI(), qrterminal.Config{
			Level: qrterminal.M, HalfBlocks: true, QuietZone: 2, Writer: writer,
		})
	} else {
		fmt.Fprintln(writer, "Station superadmin QR was not rendered because output is not an interactive terminal.")
	}
	fmt.Fprintf(writer, "Scan the one-time Station superadmin QR before %s.\n", authorization.ExpiresAt.Local().Format("2006-01-02 15:04:05 MST"))
}

func isTerminal(file *os.File) bool {
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
