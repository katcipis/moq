package moq

import (
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"strings"
	"text/template"
)

// Mocker can generate mock structs.
type Mocker struct {
	src     string
	tmpl    *template.Template
	fset    *token.FileSet
	pkgs    map[string]*ast.Package
	pkgName string
}

// New makes a new Mocker for the specified package directory.
func New(src, packageName string) (*Mocker, error) {
	fset := token.NewFileSet()
	noTestFiles := func(i os.FileInfo) bool {
		return !strings.HasSuffix(i.Name(), "_test.go")
	}
	pkgs, err := parser.ParseDir(fset, src, noTestFiles, parser.SpuriousErrors)
	if err != nil {
		return nil, err
	}
	if len(packageName) == 0 {
		for pkgName := range pkgs {
			if strings.Contains(pkgName, "_test") {
				continue
			}
			packageName = pkgName
			break
		}
	}
	if len(packageName) == 0 {
		return nil, errors.New("failed to determine package name")
	}
	tmpl, err := template.New("moq").Parse(moqTemplate)
	if err != nil {
		return nil, err
	}
	return &Mocker{
		src:     src,
		tmpl:    tmpl,
		fset:    fset,
		pkgs:    pkgs,
		pkgName: packageName,
	}, nil
}

// Mock generates a mock for the specified interface name.
func (m *Mocker) Mock(w io.Writer, name ...string) error {
	if len(name) == 0 {
		return errors.New("must specify one interface")
	}
	var objs []*obj
	for _, pkg := range m.pkgs {
		i := 0
		files := make([]*ast.File, len(pkg.Files))
		for _, file := range pkg.Files {
			files[i] = file
			i++
		}
		conf := types.Config{Importer: importer.Default()}
		tpkg, err := conf.Check(m.src, m.fset, files, nil)
		if err != nil {
			return err
		}
		for _, n := range name {
			iface := tpkg.Scope().Lookup(n)
			if iface == nil {
				return fmt.Errorf("cannot find interface %s", n)
			}
			if !types.IsInterface(iface.Type()) {
				return fmt.Errorf("%s (%s) not an interface", n, iface.Type().String())
			}
			iiface := iface.Type().Underlying().(*types.Interface).Complete()
			obj := &obj{
				InterfaceName: n,
			}
			for i := 0; i < iiface.NumMethods(); i++ {
				meth := iiface.Method(i)
				sig := meth.Type().(*types.Signature)
				method := &method{
					Name: meth.Name(),
				}
				obj.Methods = append(obj.Methods, method)
				method.Params = m.extractArgs(sig, sig.Params(), "in%d")
				method.Returns = m.extractArgs(sig, sig.Results(), "out%d")
			}
			objs = append(objs, obj)
		}
	}
	err := m.tmpl.Execute(w, struct {
		PackageName string
		Objs        []*obj
	}{
		PackageName: m.pkgName,
		Objs:        objs,
	})
	if err != nil {
		return err
	}
	return nil
}

func (m *Mocker) packageQualifier(pkg *types.Package) string {
	if m.pkgName == pkg.Name() {
		return ""
	}
	return pkg.Name()
}

func (m *Mocker) extractArgs(sig *types.Signature, list *types.Tuple, nameFormat string) []*param {
	var params []*param
	listLen := list.Len()
	for ii := 0; ii < listLen; ii++ {
		p := list.At(ii)
		name := p.Name()
		if name == "" {
			name = fmt.Sprintf(nameFormat, ii+1)
		}
		typename := types.TypeString(p.Type(), m.packageQualifier)
		// check for final variadic argument
		variadic := sig.Variadic() && ii == listLen-1 && typename[0:2] == "[]"
		param := &param{
			Name:     name,
			Type:     typename,
			Variadic: variadic,
		}
		params = append(params, param)
	}
	return params
}

type obj struct {
	InterfaceName string
	Methods       []*method
}
type method struct {
	Name    string
	Params  []*param
	Returns []*param
}

func (m *method) Arglist() string {
	params := make([]string, len(m.Params))
	for i, p := range m.Params {
		params[i] = p.String()
	}
	return strings.Join(params, ", ")
}

func (m *method) ArgCallList() string {
	params := make([]string, len(m.Params))
	for i, p := range m.Params {
		params[i] = p.CallName()
	}
	return strings.Join(params, ", ")
}

func (m *method) ReturnArglist() string {
	params := make([]string, len(m.Returns))
	for i, p := range m.Returns {
		params[i] = p.TypeString()
	}
	if len(m.Returns) > 1 {
		return fmt.Sprintf("(%s)", strings.Join(params, ", "))
	}
	return strings.Join(params, ", ")
}

type param struct {
	Name     string
	Type     string
	Variadic bool
}

func (p param) String() string {
	return fmt.Sprintf("%s %s", p.Name, p.TypeString())
}

func (p param) CallName() string {
	if p.Variadic {
		return p.Name + "..."
	}
	return p.Name
}

func (p param) TypeString() string {
	if p.Variadic {
		return "..." + p.Type[2:]
	}
	return p.Type
}

var moqTemplate = `package {{.PackageName}}

// AUTOGENERATED BY MOQ
// github.com/matryer/moq
{{ range $i, $obj := .Objs }}
// {{.InterfaceName}}Mock is a mock implementation of {{.InterfaceName}}.
//
//     func TestSomethingThatUses{{.InterfaceName}}(t *testing.T) {
//
//         // make and configure a mocked {{.InterfaceName}}
//         mocked{{.InterfaceName}} := &{{.InterfaceName}}Mock{ {{ range .Methods }}
//             {{.Name}}Func: func({{ .Arglist }}) {{.ReturnArglist}} {
// 	               panic("TODO: mock out the {{.Name}} function")
//             },{{- end }}
//         }
//
//         // TODO: use mocked{{.InterfaceName}} in code that requires {{.InterfaceName}}
//     
//     }
type {{.InterfaceName}}Mock struct {
{{- range .Methods }}
	// {{.Name}}Func mocks the {{.Name}} function.
	{{.Name}}Func func({{ .Arglist }}) {{.ReturnArglist}}
{{- end }}
}
{{ range .Methods }}
// {{.Name}} calls {{.Name}}Func.
func (mock *{{$obj.InterfaceName}}Mock) {{.Name}}({{.Arglist}}) {{.ReturnArglist}} {
	if mock.{{.Name}}Func == nil {
		panic("moq: {{$obj.InterfaceName}}Mock.{{.Name}}Func is nil but was just called")
	}
	{{ if .ReturnArglist }}
	return mock.{{.Name}}Func({{.ArgCallList}})
	{{ else }}
	mock.{{.Name}}Func({{.ArgCallList}})
	{{ end }}
}
{{ end -}}
{{ end -}}`
