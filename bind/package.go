// Copyright 2015 The go-python Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package bind

import (
	"fmt"
	"go/doc"
	"go/types"
	"reflect"
	"strings"
)

// Package ties types.Package and ast.Package together.
// Package also collects informations about structs and funcs.
type Package struct {
	pkg *types.Package
	n   int // number of entities to wrap
	sz  types.Sizes
	doc *doc.Package

	syms   *symtab
	objs   map[string]Object
	consts []Const
	vars   []Var
	types  []Type
	funcs  []Func
}

// NewPackage creates a new Package, tying types.Package and ast.Package together.
func NewPackage(pkg *types.Package, doc *doc.Package) (*Package, error) {
	universe.pkg = pkg // FIXME(sbinet)
	sz := int64(reflect.TypeOf(int(0)).Size())
	p := &Package{
		pkg:  pkg,
		n:    0,
		sz:   &types.StdSizes{sz, sz},
		doc:  doc,
		syms: newSymtab(pkg, nil),
		objs: map[string]Object{},
	}
	err := p.process()
	if err != nil {
		return nil, err
	}
	return p, err
}

// Name returns the package name.
func (p *Package) Name() string {
	return p.pkg.Name()
}

// ImportPath returns the package import path.
func (p *Package) ImportPath() string {
	return p.doc.ImportPath
}

// getDoc returns the doc string associated with types.Object
// parent is the name of the containing scope ("" for global scope)
func (p *Package) getDoc(parent string, o types.Object) string {
	n := o.Name()
	switch o.(type) {
	case *types.Const:
		for _, c := range p.doc.Consts {
			for _, cn := range c.Names {
				if n == cn {
					return c.Doc
				}
			}
		}

	case *types.Var:
		for _, v := range p.doc.Vars {
			for _, vn := range v.Names {
				if n == vn {
					return v.Doc
				}
			}
		}

	case *types.Func:
		doc := func() string {
			if o.Parent() == nil || (o.Parent() != nil && parent != "") {
				for _, typ := range p.doc.Types {
					if typ.Name != parent {
						continue
					}
					if o.Parent() == nil {
						for _, m := range typ.Methods {
							if m.Name == n {
								return m.Doc
							}
						}
					} else {
						for _, m := range typ.Funcs {
							if m.Name == n {
								return m.Doc
							}
						}
					}
				}
			} else {
				for _, f := range p.doc.Funcs {
					if n == f.Name {
						return f.Doc
					}
				}
			}
			return ""
		}()

		sig := o.Type().(*types.Signature)

		parseFn := func(tup *types.Tuple) []string {
			params := []string{}
			if tup == nil {
				return params
			}
			for i := 0; i < tup.Len(); i++ {
				paramVar := tup.At(i)
				paramType := p.syms.symtype(paramVar.Type()).pysig
				if paramVar.Name() != "" {
					paramType = fmt.Sprintf("%s %s", paramType, paramVar.Name())
				}
				params = append(params, paramType)
			}
			return params
		}

		params := parseFn(sig.Params())
		results := parseFn(sig.Results())

		paramString := strings.Join(params, ", ")
		resultString := strings.Join(results, ", ")

		//FIXME(sbinet): add receiver for methods?
		docSig := fmt.Sprintf("%s(%s) %s", o.Name(), paramString, resultString)

		if doc != "" {
			doc = fmt.Sprintf("%s\n\n%s", docSig, doc)
		} else {
			doc = docSig
		}
		return doc

	case *types.TypeName:
		for _, t := range p.doc.Types {
			if n == t.Name {
				return t.Doc
			}
		}

	default:
		// TODO(sbinet)
		panic(fmt.Errorf("not yet supported: %v (%T)", o, o))
	}

	return ""
}

// process collects informations about a go package.
func (p *Package) process() error {
	var err error

	funcs := make(map[string]Func)
	typs := make(map[string]Type)

	scope := p.pkg.Scope()
	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}

		p.n++
		p.syms.addSymbol(obj)
	}

	for _, name := range scope.Names() {
		obj := scope.Lookup(name)
		if !obj.Exported() {
			continue
		}

		switch obj := obj.(type) {
		case *types.Const:
			p.addConst(obj)

		case *types.Var:
			p.addVar(obj)

		case *types.Func:
			funcs[name], err = newFuncFrom(p, "", obj, obj.Type().(*types.Signature))
			if err != nil {
				return err
			}

		case *types.TypeName:
			typs[name], err = newType(p, obj)
			if err != nil {
				return err
			}

		default:
			//TODO(sbinet)
			panic(fmt.Errorf("not yet supported: %v (%T)", obj, obj))
		}

	}

	// remove ctors from funcs.
	// add methods.
	for tname, t := range typs {
		for name, fct := range funcs {
			if fct.Return() == nil {
				continue
			}
			if fct.Return() == t.GoType() {
				delete(funcs, name)
				fct.doc = p.getDoc(tname, scope.Lookup(name))
				fct.ctor = true
				t.ctors = append(t.ctors, fct)
				typs[tname] = t
			}
		}

		ptyp := types.NewPointer(t.GoType())
		p.syms.addType(nil, ptyp)
		mset := types.NewMethodSet(ptyp)
		for i := 0; i < mset.Len(); i++ {
			meth := mset.At(i)
			if !meth.Obj().Exported() {
				continue
			}
			m, err := newFuncFrom(p, tname, meth.Obj(), meth.Type().(*types.Signature))
			if err != nil {
				return err
			}
			t.meths = append(t.meths, m)
			if isStringer(meth.Obj()) {
				t.prots |= ProtoStringer
			}
		}
		p.addType(t)
	}

	for _, fct := range funcs {
		p.addFunc(fct)
	}

	// attach docstrings to methods
	for _, n := range p.syms.names() {
		sym := p.syms.syms[n]
		if !sym.isNamed() {
			continue
		}
		switch typ := sym.GoType().(type) {
		case *types.Named:
			for i := 0; i < typ.NumMethods(); i++ {
				m := typ.Method(i)
				if !m.Exported() {
					continue
				}
				doc := p.getDoc(sym.goname, m)
				mname := types.ObjectString(m, nil)
				msym := p.syms.sym(mname)
				if msym == nil {
					panic(fmt.Errorf(
						"gopy: could not retrieve symbol for %q",
						m.FullName(),
					))
				}
				msym.doc = doc
			}
		}
	}
	return err
}

func (p *Package) addConst(obj *types.Const) {
	p.consts = append(p.consts, newConst(p, obj))
}

func (p *Package) addVar(obj *types.Var) {
	p.vars = append(p.vars, *newVarFrom(p, obj))
}

func (p *Package) addType(t Type) {
	p.types = append(p.types, t)
	p.objs[t.GoName()] = t
}

func (p *Package) addFunc(f Func) {
	p.funcs = append(p.funcs, f)
	p.objs[f.GoName()] = f
}

// Lookup returns the bind.Object corresponding to a types.Object
func (p *Package) Lookup(o types.Object) (Object, bool) {
	obj, ok := p.objs[o.Name()]
	return obj, ok
}

// Protocol encodes the various protocols a python type may implement
type Protocol int

const (
	ProtoStringer Protocol = 1 << iota
)

// Type collects informations about a go type (struct, named-type, ...)
type Type struct {
	pkg *Package
	sym *symbol
	obj *types.TypeName

	id    string
	doc   string
	ctors []Func
	meths []Func
	funcs struct {
		new  Func
		del  Func
		init Func
		str  Func
	}

	prots Protocol
}

func newType(p *Package, obj *types.TypeName) (Type, error) {
	sym := p.syms.symtype(obj.Type())
	if sym == nil {
		panic(fmt.Errorf("no such object [%s] in symbols table", obj.Id()))
	}
	sym.doc = p.getDoc("", obj)
	typ := Type{
		pkg: p,
		sym: sym,
		obj: obj,
	}

	desc := p.ImportPath() + "." + obj.Name()
	recv := newVar(p, obj.Type(), "recv", obj.Name(), sym.doc)

	typ.funcs.new = Func{
		pkg: p,
		sig: newSignature(
			p, nil, nil,
			[]*Var{newVar(p, obj.Type(), "ret", obj.Name(), sym.doc)},
		),
		typ:  nil,
		name: obj.Name(),
		desc: desc + ".new",
		id:   sym.id + "_new",
		doc:  sym.doc,
		ret:  obj.Type(),
		err:  false,
	}

	styp := universe.sym("string")
	typ.funcs.str = Func{
		pkg: p,
		sig: newSignature(
			p, recv, nil,
			[]*Var{newVar(p, styp.GoType(), "ret", "string", "")},
		),
		typ:  nil,
		name: obj.Name(),
		desc: desc + ".str",
		id:   sym.id + "_str",
		doc:  "",
		ret:  styp.GoType(),
		err:  false,
	}

	return typ, nil
}

func (t Type) Package() *Package {
	return t.pkg
}

func (t Type) ID() string {
	return t.sym.id
}

func (t Type) Doc() string {
	return t.sym.doc
}

func (t Type) GoType() types.Type {
	return t.sym.GoType()
}

func (t Type) GoName() string {
	return t.sym.goname
}

func (t Type) Struct() *types.Struct {
	s, ok := t.sym.GoType().Underlying().(*types.Struct)
	if ok {
		return s
	}
	return nil
}

// A Signature represents a (non-builtin) function or method type.
type Signature struct {
	ret  []*Var
	args []*Var
	recv *Var
}

func newSignatureFrom(pkg *Package, sig *types.Signature) *Signature {
	var recv *Var
	if sig.Recv() != nil {
		recv = newVarFrom(pkg, sig.Recv())
	}

	return &Signature{
		ret:  newVarsFrom(pkg, sig.Results()),
		args: newVarsFrom(pkg, sig.Params()),
		recv: recv,
	}
}

func newSignature(pkg *Package, recv *Var, params, results []*Var) *Signature {
	return &Signature{
		ret:  results,
		args: params,
		recv: recv,
	}
}

func (sig *Signature) Results() []*Var {
	return sig.ret
}

func (sig *Signature) Params() []*Var {
	return sig.args
}

func (sig *Signature) Recv() *Var {
	return sig.recv
}

// Func collects informations about a go func/method.
type Func struct {
	pkg  *Package
	sig  *Signature
	typ  types.Type // if nil, Func was generated
	name string

	desc string // bind/seq descriptor
	id   string
	doc  string
	ret  types.Type // return type, if any
	err  bool       // true if original go func has comma-error
	ctor bool       // true if this is a newXXX function
}

func newFuncFrom(p *Package, parent string, obj types.Object, sig *types.Signature) (Func, error) {
	haserr := false
	res := sig.Results()
	var ret types.Type

	switch res.Len() {
	case 2:
		if !isErrorType(res.At(1).Type()) {
			return Func{}, fmt.Errorf(
				"bind: second result value must be of type error: %s",
				obj,
			)
		}
		haserr = true
		ret = res.At(0).Type()

	case 1:
		if isErrorType(res.At(0).Type()) {
			haserr = true
			ret = nil
		} else {
			ret = res.At(0).Type()
		}
	case 0:
		ret = nil
	default:
		return Func{}, fmt.Errorf("bind: too many results to return: %v", obj)
	}

	desc := obj.Pkg().Path() + "." + obj.Name()
	id := obj.Pkg().Name() + "_" + obj.Name()
	if parent != "" {
		id = obj.Pkg().Name() + "_" + parent + "_" + obj.Name()
		desc = obj.Pkg().Path() + "." + parent + "." + obj.Name()
	}

	return Func{
		pkg:  p,
		sig:  newSignatureFrom(p, sig),
		typ:  obj.Type(),
		name: obj.Name(),
		desc: desc,
		id:   id,
		doc:  p.getDoc(parent, obj),
		ret:  ret,
		err:  haserr,
	}, nil
}

func (f Func) Package() *Package {
	return f.pkg
}

func (f Func) Descriptor() string {
	return f.desc
}

func (f Func) ID() string {
	return f.id
}

func (f Func) Doc() string {
	return f.doc
}

func (f Func) GoType() types.Type {
	return f.typ
}

func (f Func) GoName() string {
	return f.name
}

func (f Func) Signature() *Signature {
	return f.sig
}

func (f Func) Return() types.Type {
	return f.ret
}

type Const struct {
	pkg *Package
	sym *symbol
	obj *types.Const
	id  string
	doc string
	f   Func
}

func newConst(p *Package, o *types.Const) Const {
	pkg := o.Pkg()
	sym := p.syms.symtype(o.Type())
	id := pkg.Name() + "_" + o.Name()
	doc := p.getDoc("", o)

	res := []*Var{newVar(p, o.Type(), "ret", o.Name(), doc)}
	sig := newSignature(p, nil, nil, res)
	fct := Func{
		pkg:  p,
		sig:  sig,
		typ:  nil,
		name: o.Name(),
		desc: p.ImportPath() + "." + o.Name() + ".get",
		id:   id + "_get",
		doc:  doc,
		ret:  o.Type(),
		err:  false,
	}

	return Const{
		pkg: p,
		sym: sym,
		obj: o,
		id:  id,
		doc: doc,
		f:   fct,
	}
}

func (c Const) Package() *Package  { return c.pkg }
func (c Const) ID() string         { return c.id }
func (c Const) Doc() string        { return c.doc }
func (c Const) GoName() string     { return c.obj.Name() }
func (c Const) GoType() types.Type { return c.obj.Type() }
