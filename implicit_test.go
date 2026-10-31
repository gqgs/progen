package progen

import (
 "context"
 "reflect"
 "testing"
)

func TestImplicitGeneratorsDeterministicAndBounded(t *testing.T) {
 d:=DefaultRecipe().Domain
 d.Resolution=24
 for _,entry:=range []struct{name string; generate func(context.Context,Domain,uint64)(*VolumeGrid,error)}{
  {"tpms",TPMS{Surface:0,Frequency:2,Thickness:.3,Phase:.2,Warp:.1,Envelope:.88}.Generate},
  {"voronoi",VoronoiFoam{Cells:20,Wall:.13,Anisotropy:1,Warp:.06,Envelope:.88}.Generate},
 } {
  a,err:=entry.generate(context.Background(),d,1234)
  if err!=nil {t.Fatal(entry.name,err)}
  b,err:=entry.generate(context.Background(),d,1234)
  if err!=nil || !reflect.DeepEqual(a.Values,b.Values) {t.Fatalf("%s not repeatable: %v",entry.name,err)}
  inside:=0
  for i,v:=range a.Values {
   if !finite(v){t.Fatalf("%s non-finite sample",entry.name)}
   if v<0 {inside++}
   x,y,z:=i%d.Resolution,(i/d.Resolution)%d.Resolution,i/(d.Resolution*d.Resolution)
   if (x==0||y==0||z==0||x==d.Resolution-1||y==d.Resolution-1||z==d.Resolution-1) && v<0 {
    t.Fatalf("%s reached boundary",entry.name)
   }
  }
  if inside<10{t.Fatalf("%s did not generate a volumetric specimen (%d voxels)",entry.name,inside)}
 }
}
func TestImplicitPresetsGenerateMesh(t *testing.T) {
 e:=testEngine(t)
 for _,p:=range RecipePresets() {
  if p.ID=="branching" {continue}
  r:=p.Recipe
  r.Domain.Resolution=32
  c,err:=e.Evaluate(context.Background(),r,"",nil)
  if err!=nil {t.Fatalf("%s generation: %v",p.ID,err)}
  if !c.Valid || c.Metrics.Triangles==0 {t.Fatalf("%s invalid: %v",p.ID,c.Problems)}
  if _,exists:=c.Artifacts["graph"];exists {t.Fatalf("%s unexpectedly generated a skeleton",p.ID)}
  if _,err:=e.Artifact(c,"volume");err!=nil {t.Fatalf("%s missing volume: %v",p.ID,err)}
 }
}
