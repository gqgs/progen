package progen

import (
 "context"
 "net/http/httptest"
 "testing"
)

func TestImplicitVolumeNeedsNoSkeleton(t *testing.T) {
 r:=DefaultRecipe()
 r.Domain.Resolution=8
 values:=make([]float64,512)
 for i:=range values { values[i]=1 }
 values[3+8*(3+8*3)]=-1
 values[4+8*(3+8*3)]=-1
 vol:=&VolumeGrid{Space:r.Domain,Values:values}
 mesh:=&Mesh{Positions:[]Vec3{{0,0,0},{.1,0,0},{0,.1,0}},Normals:[]Vec3{{0,0,1},{0,0,1},{0,0,1}},Indices:[]uint32{0,1,2}}
 m,problems,err:=(DefaultEvaluator{}).Evaluate(context.Background(),r,map[string]Artifact{
 "mesh":{Kind:"mesh",Mesh:mesh},"volume":{Kind:"volume",Volume:vol},"base":{Kind:"volume",Volume:vol},
 })
 if err!=nil || len(problems)!=0 || m.BranchFraction!=0 || m.ConnectedFraction!=1 {t.Fatalf("implicit evaluation: %+v %v %v",m,problems,err)}
}

func TestRecipePresetsAPI(t *testing.T) {
 e:=testEngine(t)
 s,err:=NewServer(e);if err!=nil{t.Fatal(err)};defer s.Close()
 w:=httptest.NewRecorder()
 s.Handler().ServeHTTP(w,httptest.NewRequest("GET","/api/recipes",nil))
 if w.Code!=200 || len(w.Body.Bytes())==0 { t.Fatalf("presets endpoint: %d %s",w.Code,w.Body.String()) }
}
