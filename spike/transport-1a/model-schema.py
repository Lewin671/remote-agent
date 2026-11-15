"""Symbolic models' wire boundary. Shared TSV also runs in all native codecs."""
import json
import ast
from pathlib import Path
RULES=json.loads((Path(__file__).parent/'wire/schema.json').read_text())
def vint(n):
    out=bytearray()
    while True:
        out.append((n&127)|(128 if n>127 else 0));n>>=7
        if not n:return bytes(out)
BOUNDS=json.loads((Path(__file__).parent/"wire/body-bounds.json").read_text())
RELATIONS=json.loads((Path(__file__).parent/"wire/body-relations.json").read_text())
def relation(node,props):
    if isinstance(node,ast.Constant):return node.value
    if isinstance(node,ast.Call):
        value=props.get(node.args[0].value)
        return value is not None if node.func.id=="has" else len(value or b"") if node.func.id=="size" else value or 0
    if isinstance(node,ast.UnaryOp):return not relation(node.operand,props)
    if isinstance(node,ast.BoolOp):return (all if isinstance(node.op,ast.And) else any)(relation(n,props) for n in node.values)
    if isinstance(node,ast.Compare):
        a,b=relation(node.left,props),relation(node.comparators[0],props);op=node.ops[0]
        return a==b if isinstance(op,ast.Eq) else a>b if isinstance(op,ast.Gt) else a>=b if isinstance(op,ast.GtE) else a<b if isinstance(op,ast.Lt) else a<=b
    raise ValueError("expression")
def normalize(raw,name,depth=0,shape=False):
    defs=RULES[name];assert depth<=8 and len(raw)<=(1048576 if name=='LogicalFrame' else 1024 if name=='Candidate' else 65536)
    at=0;last=0;values={};props={};spec={n:(k,r) for n,k,r in defs}
    def read():
        nonlocal at
        start=at;v=0
        for i in range(10):
            assert at<len(raw);b=raw[at];at+=1;assert i<9 or b<=1;v|=(b&127)<<(7*i)
            if b<128:assert raw[start:at]==vint(v);return v
        raise ValueError('integer')
    def limit(k):return 1 if k=='bool' else (1<<32)-1 if k=='u32' else (1<<63)-1 if k=='i64' else int(k[1:]) if k.startswith('e') else (1<<64)-1
    while at<len(raw):
        tag=read();n,w=tag>>3,tag&7;k,rep=spec[n];assert n>=last and (rep or n not in values);last=n;items=values.setdefault(n,[]);numeric=k!='b' and not k.startswith('m')
        if numeric and w==0:
            value=read();assert value<=limit(k);items.append(vint(n*8)+vint(value));props[n]=value
        else:
            assert w==2;size=read();assert size<=len(raw)-at;body=raw[at:at+size];at+=size
            if numeric:
                assert rep;p=0
                while p<len(body):
                    start=p;value=0
                    for i in range(10):
                        assert p<len(body);b=body[p];p+=1;assert i<9 or b<=1;value|=(b&127)<<(7*i)
                        if b<128:break
                    else:raise ValueError('packed')
                    assert body[start:p]==vint(value) and value<=limit(k);items.append(vint(n*8)+vint(value));props[n]=value
            else:
                if k.startswith('m'):normalize(body,k[1:],depth+1,shape)
                items.append(vint(n*8+2)+vint(len(body))+body);props[n]=body
        assert len(items)<=(16 if name=='Candidates' and n==1 else 2 if name=='Restart' and n==1 else 64 if name=='Capabilities' else 128)
    if shape:
        for n,(lo,hi) in BOUNDS.get(name,{}).items():
            v=props.get(int(n),0);v=len(v) if isinstance(v,bytes) else v;assert lo<=v<=hi
        if name in RELATIONS:assert relation(ast.parse(RELATIONS[name],mode='eval').body,props)
    out=b''
    for n,k,rep in defs:
        items=values.get(n,[])
        if not items and not rep and not k.startswith('m'):items=[vint(n*8+(2 if k=='b' else 0))+b'\0']
        out+=b''.join(items)
    return out
