//go:build windows

package checkout

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func filesystemIdentity(file *os.File) (string, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("windows:%x:%x:%x",
		info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}
