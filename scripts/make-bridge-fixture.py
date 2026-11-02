import json,struct,pathlib
# Deterministic fixture: a deck and two coarse side shells, meters / Y-up.
# Rebuild with python3 scripts/make-bridge-fixture.py from any directory.
p=pathlib.Path(__file__).resolve().parents[1]/'examples';p.mkdir(exist_ok=True)
positions=[];indices=[]
for a,b in [((-3.8,-.4,-.95),(3.8,0,.95)),((-3.8,-.2,.95),(3.8,1.5,1.45)),((-3.8,-.2,-1.45),(3.8,1.5,-.95))]:
 o=len(positions)
 positions += [(a[0],a[1],a[2]),(b[0],a[1],a[2]),(b[0],b[1],a[2]),(a[0],b[1],a[2]),(a[0],a[1],b[2]),(b[0],a[1],b[2]),(b[0],b[1],b[2]),(a[0],b[1],b[2])]
 for q in [(0,3,2,1),(4,5,6,7),(0,1,5,4),(3,7,6,2),(0,4,7,3),(1,2,6,5)]:
  indices += [o+q[0],o+q[1],o+q[2],o+q[0],o+q[2],o+q[3]]
bin=b''.join(struct.pack('<3f',*v) for v in positions)+b''.join(struct.pack('<H',v) for v in indices)
doc={'asset':{'version':'2.0','generator':'Progen deterministic coarse bridge fixture v1'},'scene':0,'scenes':[{'nodes':[0]}],'nodes':[{'mesh':0}],'meshes':[{'primitives':[{'attributes':{'POSITION':0},'indices':1,'mode':4}]}],'buffers':[{'byteLength':len(bin)}],'bufferViews':[{'buffer':0,'byteOffset':0,'byteLength':len(positions)*12,'target':34962},{'buffer':0,'byteOffset':len(positions)*12,'byteLength':len(indices)*2,'target':34963}],'accessors':[{'bufferView':0,'componentType':5126,'count':len(positions),'type':'VEC3','min':[min(v[i] for v in positions) for i in range(3)],'max':[max(v[i] for v in positions) for i in range(3)]},{'bufferView':1,'componentType':5123,'count':len(indices),'type':'SCALAR'}]}
j=json.dumps(doc,separators=(',',':')).encode();j+=b' '*((-len(j))%4);bin+=b'\x00'*((-len(bin))%4)
glb=struct.pack('<III',0x46546c67,2,12+8+len(j)+8+len(bin))+struct.pack('<II',len(j),0x4e4f534a)+j+struct.pack('<II',len(bin),0x004e4942)+bin
(p/'bridge.glb').write_bytes(glb)
def vec(v):return dict(zip(('X','Y','Z'),v))
def box(a,b):return {'Min':vec(a),'Max':vec(b)}
s={'version':1,'frame':'right-handed-y-up','units':'meters','transform':{'scale':1,'translation':vec((0,0,0))},'domain':{'bounds':box((-4.5,-1.5,-2),(4.5,2.5,2)),'resolution':32,'frame':'right-handed-y-up','units':'meters','boundary':'dirichlet'},'required':[box((-3.6,-.33,-.8),(3.6,-.08,.8))],'forbidden':[box((-3.7,.12,-.7),(3.7,1.8,.7))],'editable':[box((-3.9,-.3,.92),(3.9,1.6,1.5)),box((-3.9,-.3,-1.5),(3.9,1.6,-.92))],'anchors':[{'name':'west','region':box((-3.6,-.33,-.65),(-3.1,-.08,.65))},{'name':'east','region':box((3.1,-.33,-.65),(3.6,-.08,.65))}],'traversal':{'endpoints':[vec((-3.4,0,0)),vec((3.4,0,0))],'width':1.2,'height':1.6,'deck_depth':.4,'surface_tolerance':.1},'budget':{'max_triangles':200000,'max_candidates':32}}
s['parameter_bounds'] = {
 'periodic': {'surface':{'min':0,'max':2},'frequency':{'min':1.7,'max':3},'thickness':{'min':.18,'max':.32},'phase':{'min':0,'max':6.28},'warp':{'min':0,'max':.12},'envelope':{'min':.95,'max':.95}},
 'cells': {'cells':{'min':12,'max':24},'wall':{'min':.045,'max':.075},'anisotropy':{'min':.8,'max':1.3},'warp':{'min':0,'max':.05},'envelope':{'min':.95,'max':.95}},
 'combine': {'mode':{'min':0,'max':0},'blend':{'min':0,'max':0}},
 'deform': {'amplitude':{'min':0,'max':.04},'frequency':{'min':1,'max':2}}
}
(p/'bridge.constraints.json').write_text(json.dumps(s,indent=2)+'\n')

# Opt-in floor contract; preserve the original source bytes and recipe behavior.
s['floor_relief']={'region':box((-3.9,-.32,-1.02),(3.9,.24,1.02))}
s['parameter_bounds']['floor']={'strength':{'min':0,'max':1},'depth':{'min':0,'max':.06}}
(p/'bridge-relief.constraints.json').write_text(json.dumps(s,indent=2)+'\n')

# Less-protected structural experiment has its own immutable source contract.
s=json.loads((p/'bridge.constraints.json').read_text())
s['deck_policy']='structural-experiment'
s['required']=[box((-3.6,-.33,-.18),(3.6,-.12,.18))]+[a['region'] for a in s['anchors']]
s['forbidden']=[box((-3.7,.65,-.35),(3.7,1.8,.35))]
s['editable'].append(box((-3.9,-.75,-1.08),(3.9,.55,1.08)))
s['parameter_bounds']['deck']={'density':{'min':0,'max':.18},'contour':{'min':0,'max':1}}
(p/'bridge-structural.constraints.json').write_text(json.dumps(s,indent=2)+'\n')
