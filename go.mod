module github.com/nnutter/umpire

go 1.27.1

tool (
	github.com/Antonboom/testifylint
	github.com/alecthomas/go-check-sumtype/cmd/go-check-sumtype
	github.com/evilmartians/lefthook/v2
	github.com/kisielk/errcheck
	github.com/nnutter/constable/cmd/constable
	github.com/securego/gosec/v2/cmd/gosec
	github.com/zricethezav/gitleaks/v8
	go.uber.org/nilaway/cmd/nilaway
	golang.org/x/tools/cmd/goimports
	golang.org/x/vuln/cmd/govulncheck
	honnef.co/go/tools/cmd/staticcheck
	mvdan.cc/gofumpt
)
