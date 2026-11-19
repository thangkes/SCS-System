//go:build !linux

package smart

import "errors"

var errOS = errors.New("chỉ hỗ trợ Linux")

func readATAOS(string) ([]byte, []byte, error) { return nil, nil, errOS }
func readNVMeOS(string) ([]byte, error)        { return nil, errOS }
func checkDir(string) error                    { return nil }
func chgrpAgent(string) error                  { return nil }
