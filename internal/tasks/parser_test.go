package tasks

import (
	"reflect"
	"testing"
)

func TestParsePackageList(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected []PackageInfo
	}{
		{
			name: "dpkg -l format",
			output: `Desired=Unknown/Install/Remove/Purge/Hold
| Status=Not/Inst/Conf-files/Unpacked/halF-conf/Half-inst/trig-aWait/Trig-pend
|/ Err?=(none)/Reinst-required (Status,Err: uppercase=bad)
||/ Name           Version        Architecture Description
+++-==============-==============-============-=================================
ii  bash           5.2.21-2ubuntu amd64        GNU Bourne Again SHell
hi  curl           8.5.0-2ubuntu1 amd64        command line tool for transferring data
`,
			expected: []PackageInfo{
				{Name: "bash", Version: "5.2.21-2ubuntu", Architecture: "amd64", Description: "GNU Bourne Again SHell"},
				{Name: "curl", Version: "8.5.0-2ubuntu1", Architecture: "amd64", Description: "command line tool for transferring data"},
			},
		},
		{
			name: "apt list --installed format",
			output: `Listing...
bash/jammy,now 5.2.21-2ubuntu4 amd64 [installed,automatic]
curl/jammy-updates,now 8.5.0-2ubuntu10 amd64 [installed]
`,
			expected: []PackageInfo{
				{Name: "bash", Version: "5.2.21-2ubuntu4", Architecture: "amd64"},
				{Name: "curl", Version: "8.5.0-2ubuntu10", Architecture: "amd64"},
			},
		},
		{
			name: "compact line format",
			output: `bash:5.2.21-2ubuntu4
curl:8.5.0-2ubuntu10`,
			expected: []PackageInfo{
				{Name: "bash", Version: "5.2.21-2ubuntu4"},
				{Name: "curl", Version: "8.5.0-2ubuntu10"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			
			result := parsePackageList(tt.output, nil)
			
			if len(tt.expected) == 0 {
				if result != nil {
					t.Errorf("expected nil result for no packages, got %v", result)
				}
				return
			}
			
			resMap, ok := result.(map[string]any)
			if !ok {
				t.Fatalf("expected result to be map[string]any, got %T", result)
			}
			
			pkgs, ok := resMap["packages"].([]PackageInfo)
			if !ok {
				t.Fatalf("expected packages key to be []PackageInfo, got %T", resMap["packages"])
			}
			
			if !reflect.DeepEqual(pkgs, tt.expected) {
				t.Errorf("parse mismatch:\nGot: %#v\nWant: %#v", pkgs, tt.expected)
			}
		})
	}
}
