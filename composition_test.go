package progen

import (
 "context"
 "math"
 "testing"
)

func TestCSGBooleanAndSmoothOperations(t *testing.T) {
 d:=DefaultRecipe().Domain
 d.Resolution=8
 fill:=func(value float64)*VolumeGrid{
  v:=&VolumeGrid{Space:d,Values:make([]float64,512)}
  for i:=range v.Values{v.Values[i]=value}
  return v
 }
 a,b:=fill(-.2),fill(.1)
 idx:=3+8*(3+8*3)
 for _,test:=range []struct{mode int;want float64}{{0,-.2},{1,.1},{2,-.1},{3,-.2}}{
  v,err:=(CSG{Mode:test.mode,Blend:0}).Combine(context.Background(),a,b)
  if err!=nil || math.Abs(v.Values[idx]-test.want)>1e-9 {t.Fatalf("mode %d: got %v, err %v",test.mode,v,err)}
 }
 smooth,err:=(CSG{Mode:3,Blend:.4}).Combine(context.Background(),a,b)
 if err!=nil || smooth.Values[idx]>=-.2 {t.Fatalf("smooth union did not blend: %v %v",smooth,err)}
 if _,err=(CSG{Mode:5}).Combine(context.Background(),a,b);err==nil {t.Fatal("invalid mode accepted")}
}

func TestWarpIdentityAndCompositePreset(t *testing.T) {
 d:=DefaultRecipe().Domain
 d.Resolution=8
 src:=&VolumeGrid{Space:d,Values:make([]float64,512)}
 for i:=range src.Values {src.Values[i]=.1}
 src.Values[3+8*(3+8*3)]=-.8
 v,err:=(VolumeWarp{Amplitude:0,Frequency:1}).Modify(context.Background(),src)
 if err!=nil {t.Fatal(err)}
 p:=d.Point(3,3,3)
 if math.Abs(v.SignedValue(p)-src.SignedValue(p))>1e-10 {t.Fatal("zero deformation changed interior")}
 e:=testEngine(t)
 recipe:=HybridRecipe()
 recipe.Domain.Resolution=32
 c,err:=e.Evaluate(context.Background(),recipe,"",nil)
 if err!=nil || !c.Valid || c.Metrics.Triangles==0 {t.Fatalf("hybrid evaluation: %v %v",c,err)}
 if len(c.Nodes)!=5 || len(c.Artifacts)!=3 {t.Fatal("wrong compositional artifacts",c.Nodes,c.Artifacts)}
}
