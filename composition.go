package progen

import (
 "context"
 "fmt"
 "math"
)

// CSG combines two implicit fields, keeping negative values inside.
// Smooth union uses level-set units: inputs are not necessarily true SDFs.
type CSG struct { Mode int; Blend float64 }
func (op CSG) Combine(ctx context.Context,a,b Volume)(*VolumeGrid,error){
 if op.Mode<0||op.Mode>3||op.Blend<0 {return nil,fmt.Errorf("invalid CSG parameters")}
 d:=a.Domain()
 if !sameDomain(d,b.Domain()){return nil,fmt.Errorf("CSG inputs must use the same domain")}
 n:=d.Resolution
 v:=&VolumeGrid{Space:d,Values:make([]float64,n*n*n)}
 for z:=0;z<n;z++{
  if err:=ctx.Err();err!=nil{return nil,err}
  for y:=0;y<n;y++{for x:=0;x<n;x++{
   p:=d.Point(x,y,z)
   left,right:=a.SignedValue(p),b.SignedValue(p)
   var out float64
   switch op.Mode{
   case 0:out=math.Min(left,right)
   case 1:out=math.Max(left,right)
   case 2:out=math.Max(left,-right)
   case 3:
    out=math.Min(left,right)
    if op.Blend>0 {
     h:=math.Max(op.Blend-math.Abs(left-right),0)/op.Blend
     out-=h*h*op.Blend*.25
    }
   }
   v.Values[x+n*(y+n*z)]=out
  }}
 }
 applyConstraints(v)
 return v,nil
}

// VolumeWarp samples a volume through a smooth three-dimensional coordinate
// deformation, retaining the original domain and exclusion constraints.
type VolumeWarp struct { Amplitude, Frequency float64 }
func (op VolumeWarp) Modify(ctx context.Context,src Volume)(*VolumeGrid,error){
 if op.Amplitude<0||op.Frequency<=0{return nil,fmt.Errorf("invalid warp parameters")}
 d:=src.Domain()
 n:=d.Resolution
 half:=d.Box.Max.Sub(d.Box.Min).Mul(.5)
 v:=&VolumeGrid{Space:d,Values:make([]float64,n*n*n)}
 for z:=0;z<n;z++{
  if err:=ctx.Err();err!=nil{return nil,err}
  for y:=0;y<n;y++{for x:=0;x<n;x++{
   p:=d.Point(x,y,z)
   q:=normalizedPoint(d,p).Mul(op.Frequency*math.Pi)
   delta:=Vec3{
    op.Amplitude*half.X*math.Sin(q.Y)*math.Sin(q.Z),
    op.Amplitude*half.Y*math.Sin(q.Z)*math.Sin(q.X),
    op.Amplitude*half.Z*math.Sin(q.X)*math.Sin(q.Y),
   }
   v.Values[x+n*(y+n*z)]=src.SignedValue(p.Add(delta))
  }}
 }
 applyConstraints(v)
 return v,nil
}
