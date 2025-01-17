// Copyright 2015 The go-python Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
)

type pkg struct {
	path string
	want []byte
}

func testPkg(t *testing.T, table pkg) {
	workdir, err := ioutil.TempDir("", "gopy-")
	if err != nil {
		t.Fatalf("[%s]: could not create workdir: %v\n", table.path, err)
	}
	err = os.MkdirAll(workdir, 0644)
	if err != nil {
		t.Fatalf("[%s]: could not create workdir: %v\n", table.path, err)
	}
	defer os.RemoveAll(workdir)

	cmd := exec.Command("gopy", "bind", "-output="+workdir, "./"+table.path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		t.Fatalf("[%s]: error running gopy-bind: %v\n", table.path, err)
	}

	cmd = exec.Command(
		"/bin/cp", "./"+table.path+"/test.py",
		filepath.Join(workdir, "test.py"),
	)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		t.Fatalf("[%s]: error copying 'test.py': %v\n", table.path, err)
	}

	buf := new(bytes.Buffer)
	cmd = exec.Command("python2", "./test.py")
	cmd.Dir = workdir
	cmd.Stdin = os.Stdin
	cmd.Stdout = buf
	cmd.Stderr = buf
	err = cmd.Run()
	if err != nil {
		t.Fatalf(
			"[%s]: error running python module: %v\n%v\n",
			table.path,
			err,
			string(buf.Bytes()),
		)
	}

	if !reflect.DeepEqual(string(buf.Bytes()), string(table.want)) {
		diffTxt := ""
		diffBin, diffErr := exec.LookPath("diff")
		if diffErr == nil {
			wantFile, wantErr := os.Create(filepath.Join(workdir, "want.txt"))
			if wantErr == nil {
				wantFile.Write(table.want)
				wantFile.Close()
			}
			gotFile, gotErr := os.Create(filepath.Join(workdir, "got.txt"))
			if gotErr == nil {
				gotFile.Write(buf.Bytes())
				gotFile.Close()
			}
			if gotErr == nil && wantErr == nil {
				cmd = exec.Command(diffBin, "-urN",
					wantFile.Name(),
					gotFile.Name(),
				)
				diff, _ := cmd.CombinedOutput()
				diffTxt = string(diff) + "\n"
			}
		}

		t.Fatalf("[%s]: error running python module:\nwant:\n%s\n\ngot:\n%s\n%s",
			table.path,
			string(table.want), string(buf.Bytes()),
			diffTxt,
		)
	}

}

func TestHi(t *testing.T) {
	t.Skip("bind/seq") // FIXME(sbinet)
	t.Parallel()

	testPkg(t, pkg{
		path: "_examples/hi",
		want: []byte(`--- doc(hi)...
package hi exposes a few Go functions to be wrapped and used from Python.

--- hi.GetUniverse(): 42
--- hi.GetVersion(): 0.1
--- hi.GetDebug(): False
--- hi.SetDebug(true)
--- hi.GetDebug(): True
--- hi.SetDebug(false)
--- hi.GetDebug(): False
--- hi.GetAnon(): hi.Person{Name="<nobody>", Age=1}
--- new anon: hi.Person{Name="you", Age=24}
--- hi.SetAnon(hi.NewPerson('you', 24))...
--- hi.GetAnon(): hi.Person{Name="you", Age=24}
--- doc(hi.Hi)...
Hi() 

Hi prints hi from Go

--- hi.Hi()...
hi from go
--- doc(hi.Hello)...
Hello(str s) 

Hello prints a greeting from Go

--- hi.Hello('you')...
hello you from go
--- doc(hi.Add)...
Add(int i, int j) int

Add returns the sum of its arguments.

--- hi.Add(1, 41)...
42
--- hi.Concat('4', '2')...
42
--- doc(hi.Person):
Person is a simple struct

--- p = hi.Person()...
['Age', 'Greet', 'Name', 'Salary', 'String', 'Work', '__class__', '__delattr__', '__doc__', '__format__', '__getattribute__', '__hash__', '__init__', '__new__', '__reduce__', '__reduce_ex__', '__repr__', '__setattr__', '__sizeof__', '__str__', '__subclasshook__']
--- p: hi.Person{Name="", Age=0}
--- p.Name: 
--- p.Age: 0
--- doc(hi.Greet):
Greet() str

Greet sends greetings

--- p.Greet()...
Hello, I am 
--- p.String()...
hi.Person{Name="", Age=0}
--- doc(p):
Person is a simple struct

--- p.Name = "foo"...
--- p.Age = 42...
--- p.String()...
hi.Person{Name="foo", Age=42}
--- p.Age: 42
--- p.Name: foo
--- p.Work(2)...
working...
worked for 2 hours
--- p.Work(24)...
working...
caught: can't work for 24 hours!
--- p.Salary(2): 20
--- p.Salary(24): caught: can't work for 24 hours!
--- Person.__init__
caught: invalid type for 'Name' attribute | err-type: <type 'exceptions.TypeError'>
caught: invalid type for 'Age' attribute | err-type: <type 'exceptions.TypeError'>
caught: Person.__init__ takes at most 2 argument(s) | err-type: <type 'exceptions.TypeError'>
hi.Person{Name="name", Age=0}
hi.Person{Name="name", Age=42}
hi.Person{Name="name", Age=42}
hi.Person{Name="name", Age=42}
--- hi.NewPerson('me', 666): hi.Person{Name="me", Age=666}
--- hi.NewPersonWithAge(666): hi.Person{Name="stranger", Age=666}
--- hi.NewActivePerson(4):working...
worked for 4 hours
 hi.Person{Name="", Age=0}
--- c = hi.Couple()...
hi.Couple{P1=hi.Person{Name="", Age=0}, P2=hi.Person{Name="", Age=0}}
--- c.P1: hi.Person{Name="", Age=0}
--- c: hi.Couple{P1=hi.Person{Name="tom", Age=5}, P2=hi.Person{Name="bob", Age=2}}
--- c = hi.NewCouple(tom, bob)...
hi.Couple{P1=hi.Person{Name="tom", Age=50}, P2=hi.Person{Name="bob", Age=41}}
hi.Couple{P1=hi.Person{Name="mom", Age=50}, P2=hi.Person{Name="bob", Age=51}}
--- Couple.__init__
hi.Couple{P1=hi.Person{Name="p1", Age=42}, P2=hi.Person{Name="", Age=0}}
hi.Couple{P1=hi.Person{Name="p1", Age=42}, P2=hi.Person{Name="p2", Age=52}}
hi.Couple{P1=hi.Person{Name="p1", Age=42}, P2=hi.Person{Name="p2", Age=52}}
hi.Couple{P1=hi.Person{Name="p2", Age=52}, P2=hi.Person{Name="p1", Age=42}}
caught: invalid type for 'P1' attribute | err-type: <type 'exceptions.TypeError'>
caught: invalid type for 'P1' attribute | err-type: <type 'exceptions.TypeError'>
caught: invalid type for 'P2' attribute | err-type: <type 'exceptions.TypeError'>
--- testing GC...
--- len(objs): 100000
--- len(vs): 100000
--- testing GC... [ok]
--- testing array...
arr: [2]int{1, 2}
len(arr): 2
arr[0]: 1
arr[1]: 2
arr[2]: caught: array index out of range
arr: [2]int{1, 42}
len(arr): 2
mem(arr): 2
--- testing slice...
slice: []int{1, 2}
len(slice): 2
slice[0]: 1
slice[1]: 2
slice[2]: caught: array index out of range
slice: []int{1, 42}
len(slice): 2
mem(slice): 2
`),
	})
}

func TestBindFuncs(t *testing.T) {
	t.Skip("bind/seq") // FIXME(sbinet)
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/funcs",
		want: []byte(`funcs.GetF1()...
calling F1
f1()= None
funcs.GetF2()...
calling F2
f2()= None
s1 = funcs.S1()...
s1.F1 = funcs.GetF2()...
calling F2
s1.F1() = None
s2 = funcs.S2()...
s2.F1 = funcs.GetF1()...
calling F1
s2.F1() = None
`),
	})
}

func TestBindSimple(t *testing.T) {
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/simple",
		want: []byte(`doc(pkg):
'simple is a simple package.\n'
pkg.Func()...
fct = pkg.Func...
fct()...
pkg.Add(1,2)= 3
`),
	})
}

func TestBindEmpty(t *testing.T) {
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/empty",
		want: []byte(`empty.init()... [CALLED]
doc(pkg):
'Package empty does not expose anything.\nWe may want to wrap and import it just for its side-effects.\n'
`),
	})
}

func TestBindPointers(t *testing.T) {
	t.Skip("not ready yet")
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/pointers",
		want: []byte(`s = pointers.S(2)
s = pointers.S{Value:2}
s.Value = 2
pointers.Inc(s)
==> go: s.Value==2
<== go: s.Value==3
s.Value = 3
`),
	})
}

func TestBindNamed(t *testing.T) {
	t.Skip("bind/seq") // FIXME(sbinet)
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/named",
		want: []byte(`doc(named): 'package named tests various aspects of named types.\n'
doc(named.Float): ''
doc(named.Float.Value): 'Value() float\n\nValue returns a float32 value\n'
v = named.Float()
v = 0
v.Value() = 0.0
x = named.X()
x = 0
x.Value() = 0.0
x = named.XX()
x = 0
x.Value() = 0.0
x = named.XXX()
x = 0
x.Value() = 0.0
x = named.XXXX()
x = 0
x.Value() = 0.0
v = named.Float(42)
v = 42
v.Value() = 42.0
v = named.Float(42.0)
v = 42
v.Value() = 42.0
x = named.X(42)
x = 42
x.Value() = 42.0
x = named.XX(42)
x = 42
x.Value() = 42.0
x = named.XXX(42)
x = 42
x.Value() = 42.0
x = named.XXXX(42)
x = 42
x.Value() = 42.0
x = named.XXXX(42.0)
x = 42
x.Value() = 42.0
s = named.Str()
s = ""
s.Value() = ''
s = named.Str('string')
s = "string"
s.Value() = 'string'
arr = named.Array()
arr = named.Array{0, 0}
arr = named.Array([1,2])
arr = named.Array{1, 2}
arr = named.Array(range(10))
caught: Array.__init__ takes a sequence of size at most 2
arr = named.Array(xrange(2))
arr = named.Array{0, 1}
s = named.Slice()
s = named.Slice(nil)
s = named.Slice([1,2])
s = named.Slice{1, 2}
s = named.Slice(range(10))
s = named.Slice{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
s = named.Slice(xrange(10))
s = named.Slice{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
`),
	})
}

func TestBindStructs(t *testing.T) {
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/structs",
		want: []byte(`s = structs.S()
s = structs.S{}
s.Init()
s.Upper('boo')= 'BOO'
s1 = structs.S1()
s1 = structs.S1{private:0}
caught error: 'structs.S1' object has no attribute 'private'
s2 = structs.S2()
s2 = structs.S2{Public:0, private:0}
s2 = structs.S2(1)
s2 = structs.S2{Public:1, private:0}
caught error: S2.__init__ takes at most 1 argument(s)
s2 = structs.S2{Public:42, private:0}
s2.Public = 42
caught error: 'structs.S2' object has no attribute 'private'
`),
	})
}

func TestBindConsts(t *testing.T) {
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/consts",
		want: []byte(`c1 = c1
c2 = 42
c3 = 666.666
c4 = c4
c5 = 42
c6 = 42
c7 = 666.666
k1 = 1
k2 = 2
`),
	})
}

func TestBindVars(t *testing.T) {
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/vars",
		want: []byte(`v1 = v1
v2 = 42
v3 = 666.666
v4 = c4
v5 = 42
v6 = 42
v7 = 666.666
k1 = 1
k2 = 2
v1 = -v1-
v2 = 4242
v3 = -666.666
v4 = -c4-
v5 = 24
v6 = 24
v7 = -666.666
k1 = 11
k2 = 22
`),
	})
}

func TestBindSeqs(t *testing.T) {
	t.Skip("bind/seq") // FIXME(sbinet)
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/seqs",
		want: []byte(`doc(seqs): 'package seqs tests various aspects of sequence types.\n'
arr = seqs.Array(xrange(2))
arr = seqs.Array{0, 1, 0, 0, 0, 0, 0, 0, 0, 0}
s = seqs.Slice()
s = seqs.Slice(nil)
s = seqs.Slice([1,2])
s = seqs.Slice{1, 2}
s = seqs.Slice(range(10))
s = seqs.Slice{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
s = seqs.Slice(xrange(10))
s = seqs.Slice{0, 1, 2, 3, 4, 5, 6, 7, 8, 9}
s = seqs.Slice()
s = seqs.Slice(nil)
s += [1,2]
s = seqs.Slice{1, 2}
s += [10,20]
s = seqs.Slice{1, 2, 10, 20}
`),
	})
}

func TestBindInterfaces(t *testing.T) {
	t.Skip("not ready")
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/iface",
		want: []byte(`
`),
	})
}

func TestBindCgoPackage(t *testing.T) {
	t.Skip("bind/seq") // FIXME(sbinet)
	t.Parallel()
	testPkg(t, pkg{
		path: "_examples/cgo",
		want: []byte(`cgo.doc: 'Package cgo tests bindings of CGo-based packages.\n'
cgo.Hi()= 'hi from go\n'
cgo.Hello(you)= 'hello you from go\n'
`),
	})
}
