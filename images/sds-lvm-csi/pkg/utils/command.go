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

func LVExist(vgName, lvName string) (command string, stdErr bytes.Buffer, err error) {
	var outs bytes.Buffer
	lv := fmt.Sprintf("/dev/%s/%s", vgName, lvName)
	cmd := exec.Command("lvdisplay", lv)
	cmd.Stdout = &outs
	cmd.Stderr = &stdErr

	if err := cmd.Run(); err != nil {
		return cmd.String(), stdErr, fmt.Errorf("lv %s in not exist, err: %w", lv, err)
	}

	return cmd.String(), stdErr, nil
}
