package progen

import (
 "context"
 "encoding/json"
 "errors"
 "net/http/httptest"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"
)

func flakyMeshEngine(t *testing.T, fatal bool) (*Engine, Recipe) {
 t.Helper()
 e := testEngine(t)
 message := error(ErrMeshTriangleLimit)
 if fatal { message = errors.New("simulated storage failure") }
 err := e.Registry.Register(Descriptor{
  ID:"test-budget-mesh", Version:"1",
  Inputs:map[string]Port{"volume":{Kind:"volume", Semantic:"solid"}},
  Outputs:map[string]Port{"mesh":{Kind:"mesh", Semantic:"surface"}},
  Parameters:map[string]Param{
   "trigger":{Type:"number",Default:1,Min:0,Max:1,Mutable:true},
  },
 }, func(p map[string]float64)(Operator,error){
  return OperatorFunc(func(ctx context.Context,_ Domain,_ uint64,in Inputs)(Outputs,error){
   if p["trigger"]==1 {return nil,message}
   m,err:=(Tetrahedra{}).Mesh(ctx,in["volume"].Volume)
   return Outputs{"mesh":{Kind:"mesh",Mesh:m}},err
  }),nil
 })
 if err != nil {t.Fatal(err)}
 r:=ImplicitRecipe("tpms")
 r.Domain.Resolution=32
 r.Nodes[1].Operator="test-budget-mesh"
 r.Nodes[1].Params=map[string]float64{"trigger":1}
 return e,r
}

func TestBudgetFailureSkipsOnlyOneCandidate(t *testing.T) {
 for _,workers:=range []int{1,3}{
  t.Run(string(rune('0'+workers)),func(t *testing.T){
   e,r:=flakyMeshEngine(t,false)
   progress:=0
   successes, failures, err:=e.RunWithFailures(context.Background(), RunRequest{
    Kind:"generate",Recipe:&r,Count:4,SearchSeed:7,Workers:workers,
   },func(done,total int,stage string){if done>progress{progress=done}})
   if err!=nil {t.Fatal(err)}
   if len(successes)!=3 || len(failures)!=1 || failures[0].Index!=0 {
    t.Fatalf("expected 3 successful specimens and one failed: %d %v",len(successes),failures)
   }
   if !strings.Contains(failures[0].Reason,"two million triangles") || failures[0].Recipe.Nodes[1].Params["trigger"]!=1 {
    t.Fatal("missing reproducible limit failure",failures)
   }
   if progress!=4 {t.Fatalf("progress counted only successful candidates: %d",progress)}
   for _,c:=range successes{if c==nil || !c.Valid {t.Fatal("invalid survivor",c)}}
   names,err:=os.ReadDir(filepath.Join(e.Workspace,"runs"))
   if err!=nil || len(names)!=1 {t.Fatalf("missing run manifest: %v %d",err,len(names))}
   data,err:=os.ReadFile(filepath.Join(e.Workspace,"runs",names[0].Name()))
   if err!=nil {t.Fatal(err)}
   var record RunRecord
   if err=json.Unmarshal(data,&record);err!=nil {t.Fatal(err)}
   if record.Status!="completed" || len(record.Failures)!=1 || len(record.Candidates)!=3 {
    t.Fatalf("incorrect completed run status: %+v",record)
   }
  })
 }
}

func TestNonBudgetErrorStillFails(t *testing.T){
 e,r:=flakyMeshEngine(t,true)
 _,failures,err:=e.RunWithFailures(context.Background(),RunRequest{
  Kind:"generate",Recipe:&r,Count:2,Workers:1,SearchSeed:7,
 },nil)
 if err==nil || !strings.Contains(err.Error(),"simulated storage failure") || len(failures)!=0 {
  t.Fatalf("unexpected fatal-error semantics: %v %v",err,failures)
 }
}

func TestBudgetSkipsDuringMAPElites(t *testing.T){
 e,r:=flakyMeshEngine(t,false)
 // The search mutates params, so freeze this test's trigger at the failing value.
 entry:=e.Registry.entries["test-budget-mesh"]
 spec:=entry.desc.Parameters["trigger"]
 spec.Min,spec.Max,spec.Default,spec.Mutable=1,1,1,false
 entry.desc.Parameters["trigger"]=spec
 e.Registry.entries["test-budget-mesh"]=entry
 results,failures,err:=e.RunWithFailures(context.Background(),RunRequest{
  Kind:"search",Recipe:&r,Count:3,SearchSeed:11,
 },nil)
 if err!=nil || len(results)!=0 || len(failures)!=3 {t.Fatalf("search should skip all oversize specimens: %v %d %d",err,len(results),len(failures))}
 for i,f:=range failures{if f.Index!=i{t.Fatal("missing failure index",failures)}}
}

func TestJobReportsBudgetSkipsWithoutFailure(t *testing.T){
 e,r:=flakyMeshEngine(t,false)
 srv,err:=NewServer(e);if err!=nil{t.Fatal(err)}
 defer srv.Close()
 body,_:=json.Marshal(RunRequest{Kind:"generate",Recipe:&r,Count:3,Workers:2,SearchSeed:7})
 http:=srv.Handler()
 w:=httptest.NewRecorder()
 http.ServeHTTP(w,httptest.NewRequest("POST","/api/jobs",strings.NewReader(string(body))))
 if w.Code!=202 {t.Fatal(w.Body.String())}
 var job Job
 if err=json.Unmarshal(w.Body.Bytes(),&job);err!=nil{t.Fatal(err)}
 deadline:=time.Now().Add(30*time.Second)
 for{
  w=httptest.NewRecorder()
  http.ServeHTTP(w,httptest.NewRequest("GET","/api/jobs/"+job.ID,nil))
  if w.Code!=200{t.Fatal(w.Body.String())}
  if err=json.Unmarshal(w.Body.Bytes(),&job);err!=nil{t.Fatal(err)}
  if job.Status=="completed" || job.Status=="failed" || job.Status=="cancelled"{break}
  if time.Now().After(deadline){t.Fatal("job timeout")}
  time.Sleep(20*time.Millisecond)
 }
 if job.Status!="completed" || job.Error!="" || job.Completed!=3 || job.Skipped!=1 || len(job.Failures)!=1 || len(job.Candidates)!=2 {
  t.Fatalf("incorrect job status: %+v",job)
 }
}
