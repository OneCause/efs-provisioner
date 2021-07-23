package internal

import (
	"fmt"

	"k8s.io/klog/v2"
)

func LogErrorf(format string, a ...interface{}) error {
	msg := fmt.Sprintf(format, a...)
	klog.Error(msg)
	return fmt.Errorf(msg)
}
