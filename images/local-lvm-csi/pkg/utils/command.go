package utils

import (
	"bytes"
	"fmt"
	"os/exec"
)

func CreateLV(size, pvName, VGName string) (string, error) {

	cmd := exec.Command(
		"lvcreate", "-L", size, "-n", pvName, VGName)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return cmd.String(), fmt.Errorf("unable to CreateLV, err: %w tderr = %s", err, stderr.String())
	}
	return cmd.String(), nil
}

//func Mount(fsType, device, target string) (string, error) {
//
//	cmd := exec.Command(
//		"mount", "-t", fsType, device, target)
//
//	var stderr bytes.Buffer
//	cmd.Stderr = &stderr
//
//	if err := cmd.Run(); err != nil {
//		return cmd.String(), fmt.Errorf("unable to Mount, err: %w tderr = %s", err, stderr.String())
//	}
//	return cmd.String(), nil
//}
