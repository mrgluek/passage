package main

import "strings"

// linkPipePath is the import pipe of one user.
func linkPipePath(sid string) string { return `\\.\pipe\PassageImport-` + sid }

// linkPipeSDDL gives the pipe to the user alone: the owner and the only
// access entry, so another account can neither connect nor take the name.
func linkPipeSDDL(sid string) string {
	return "O:" + sid + "D:P(A;;GA;;;" + sid + ")"
}

// validSID accepts the text form of a SID, which is safe inside an SDDL string.
func validSID(sid string) bool {
	if !strings.HasPrefix(sid, "S-1-") || len(sid) > 184 {
		return false
	}
	for _, c := range sid[4:] {
		if c != '-' && (c < '0' || c > '9') {
			return false
		}
	}
	return !strings.HasSuffix(sid, "-")
}
