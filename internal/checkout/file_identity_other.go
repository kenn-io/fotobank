//go:build !windows

package checkout

import (
	"fmt"
	"os"
	"reflect"
)

// filesystemIdentity returns the device and inode exposed by Unix-like file
// systems. Ports without those fields return an empty identity; the scanner
// then hashes instead of trusting size and modification time alone.
func filesystemIdentity(file *os.File) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	value := reflect.ValueOf(info.Sys())
	for value.IsValid() && (value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface) {
		if value.IsNil() {
			return "", nil
		}
		value = value.Elem()
	}
	if !value.IsValid() || value.Kind() != reflect.Struct {
		return "", nil
	}
	device, deviceOK := identityInteger(value.FieldByName("Dev"))
	inode, inodeOK := identityInteger(value.FieldByName("Ino"))
	if !deviceOK || !inodeOK {
		return "", nil
	}
	return fmt.Sprintf("unix:%x:%x", device, inode), nil
}

func identityInteger(value reflect.Value) (uint64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Int() < 0 {
			return 0, false
		}
		return uint64(value.Int()), true
	default:
		return 0, false
	}
}
