package progen

import (
 "context"
 "math"
 "testing"
)

func TestImplicitDescriptorAxesAndArchiveIsolation(t *testing.T) {
 e:=testEngine(t)
 tree:=DefaultRecipe()
 tpms:=ImplicitRecipe("tpms")
 other:=ImplicitRecipe("voronoi_foam")
 td,err:=e.Archive(tree)
 if err!=nil || td.DescriptorVersion!="morphology-v2" {t.Fatalf("tree descriptor changed: %v %v",td,err)}
 for _,r:=range []Recipe{tpms,other,HybridRecipe()} {
  a,err:=e.Archive(r)
  if err!=nil || a.DescriptorVersion!="implicit-morphology-v1" || a.Compatible(td) {t.Fatalf("wrong implicit archive: %v %v",a,err)}
 }
 a:=Candidate{ID:"0000000000000000000000000000000000000000000000000000000000000abc",Recipe:tpms,Valid:true,Metrics:Metrics{SolidFraction:.4,SurfaceFraction:.6,ConnectedFraction:.9}}
 saveFixture(t,e,a)
 archive,err:=e.Archive(tpms)
 if err!=nil || archive.Cells["6,9"]!=a.ID {t.Fatalf("wrong volume cell: %v %v",archive.Cells,err)}
 if got,_:=e.Archive(other);len(got.Cells)!=0 {t.Fatal("mixed family archives",got.Cells)}
 if got,_:=e.Archive(tree);len(got.Cells)!=0 {t.Fatal("mixed tree archive",got.Cells)}
}

func TestImplicitMetricsOneVoxel(t *testing.T) {
 r:=DefaultRecipe()
 r.Domain.Resolution=8
 values:=make([]float64,512)
 for i:=range values {values[i]=1}
 values[3+8*(3+8*3)]=-1
 v:=&VolumeGrid{Space:r.Domain,Values:values}
 m:=&Mesh{Positions:[]Vec3{{0,0,0},{.1,0,0},{0,.1,0}},Normals:[]Vec3{{0,0,1},{0,0,1},{0,0,1}},Indices:[]uint32{0,1,2}}
 metrics,problems,err:=(DefaultEvaluator{}).Evaluate(context.Background(),r,map[string]Artifact{
 "mesh":{Kind:"mesh",Mesh:m},"base":{Kind:"volume",Volume:v},"volume":{Kind:"volume",Volume:v},
 })
 if err!=nil || len(problems)!=0 {t.Fatalf("invalid fixture: %v %v",problems,err)}
 if math.Abs(metrics.SolidFraction-1.0/512)>1e-10 || metrics.SurfaceFraction!=1 {
  t.Fatalf("incorrect volume metrics: %+v",metrics)
 }
}

func TestImplicitSearchArchive(t *testing.T) {
 e:=testEngine(t)
 r:=ImplicitRecipe("tpms")
 r.Domain.Resolution=24
 generated,err:=e.Run(context.Background(),RunRequest{Kind:"search",Recipe:&r,Count:3,SearchSeed:47},nil)
 if err!=nil || len(generated)!=3 {t.Fatalf("implicit search: %v %v",generated,err)}
 a,err:=e.Archive(r)
 if err!=nil || a.DescriptorVersion!="implicit-morphology-v1" || len(a.Cells)==0 {t.Fatalf("implicit archive: %v %v",a,err)}
 for _,c:=range generated {
  if c!=nil && c.Valid && (c.Metrics.SolidFraction<=0 || c.Metrics.SurfaceFraction<=0) {t.Fatalf("missing morphology metrics: %+v",c.Metrics)}
 }
}
