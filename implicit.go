package progen

import (
 "context"
 "fmt"
 "math"
 "math/rand/v2"
)

// Implicit operators sample inside-negative scalar volumes without a skeleton.
// Values are level-set functions, not guaranteed Euclidean signed distances.
type TPMS struct {
 Surface int
 Frequency, Thickness, Phase, Warp, Envelope float64
}
func normalizedPoint(d Domain, p Vec3) Vec3 {
 mid:=d.Box.Min.Add(d.Box.Max).Mul(.5)
 half:=d.Box.Max.Sub(d.Box.Min).Mul(.5)
 v:=p.Sub(mid)
 return Vec3{v.X/half.X,v.Y/half.Y,v.Z/half.Z}
}
func (op TPMS) Generate(ctx context.Context,d Domain,seed uint64)(*VolumeGrid,error){
 if op.Surface<0||op.Surface>2||op.Frequency<=0||op.Thickness<=0||op.Envelope<=0||op.Envelope>1||op.Warp<0{
  return nil,fmt.Errorf("invalid TPMS parameters")
 }
 n:=d.Resolution
 values:=make([]float64,n*n*n)
 rng:=rand.New(rand.NewPCG(seed,seed^0x9e3779b97f4a7c15))
 px,py,pz:=op.Phase+0.35*rng.Float64(),op.Phase*.79+0.35*rng.Float64(),op.Phase*1.13+0.35*rng.Float64()
 for z:=0;z<n;z++{
  if err:=ctx.Err();err!=nil{return nil,err}
  for y:=0;y<n;y++{for x:=0;x<n;x++{
   p:=normalizedPoint(d,d.Point(x,y,z))
   a:=p.Mul(math.Pi*op.Frequency)
   ax:=a.X+px+op.Warp*math.Sin(a.Y+a.Z)
   ay:=a.Y+py+op.Warp*math.Sin(a.Z+a.X)
   az:=a.Z+pz+op.Warp*math.Sin(a.X+a.Y)
   var f float64
   switch op.Surface{
   case 0: f=math.Sin(ax)*math.Cos(ay)+math.Sin(ay)*math.Cos(az)+math.Sin(az)*math.Cos(ax) // gyroid
   case 1: f=math.Cos(ax)+math.Cos(ay)+math.Cos(az) // Schwarz P
   case 2: f=math.Sin(ax)*math.Sin(ay)*math.Sin(az)+math.Sin(ax)*math.Cos(ay)*math.Cos(az)+math.Cos(ax)*math.Sin(ay)*math.Cos(az)+math.Cos(ax)*math.Cos(ay)*math.Sin(az) // diamond
   }
   // Envelope clips the periodic structure into a finite inspectable specimen.
   values[x+n*(y+n*z)]=math.Max(math.Abs(f)-op.Thickness,p.Len()-op.Envelope)
  }}
 }
 v:=&VolumeGrid{Space:d,Values:values}
 applyConstraints(v)
 return v,nil
}

type VoronoiFoam struct {
 Cells int
 Wall, Anisotropy, Warp, Envelope float64
}
func (op VoronoiFoam) Generate(ctx context.Context,d Domain,seed uint64)(*VolumeGrid,error){
 if op.Cells<4||op.Cells>128||op.Wall<=0||op.Anisotropy<=0||op.Warp<0||op.Envelope<=0||op.Envelope>1{
  return nil,fmt.Errorf("invalid foam parameters")
 }
 rng:=rand.New(rand.NewPCG(seed,seed^0x517cc1b727220a95))
 sites:=make([]Vec3,0,op.Cells)
 for len(sites)<op.Cells {
  p:=Vec3{rng.Float64()*2-1,rng.Float64()*2-1,rng.Float64()*2-1}
  if p.Len()<.96 { sites=append(sites,p.Mul(op.Envelope)) }
 }
 n:=d.Resolution
 values:=make([]float64,n*n*n)
 for z:=0;z<n;z++{
  if err:=ctx.Err();err!=nil{return nil,err}
  for y:=0;y<n;y++{for x:=0;x<n;x++{
   p:=normalizedPoint(d,d.Point(x,y,z))
   warped:=Vec3{
    p.X+op.Warp*math.Sin(5*p.Y+3*p.Z),
    p.Y+op.Warp*math.Sin(5*p.Z+3*p.X),
    p.Z+op.Warp*math.Sin(5*p.X+3*p.Y),
   }
   d1,d2:=math.MaxFloat64,math.MaxFloat64
   for _,site:=range sites {
    delta:=warped.Sub(site)
    dist:=delta.X*delta.X+delta.Y*delta.Y*op.Anisotropy*op.Anisotropy+delta.Z*delta.Z
    if dist<d1 {d2=d1;d1=dist} else if dist<d2 {d2=dist}
   }
   // Neighboring Voronoi cells meet where their two closest distances coincide.
   // Thickening this interface yields a continuous cellular wall network.
   wall:=math.Sqrt(d2)-math.Sqrt(d1)-op.Wall
   values[x+n*(y+n*z)]=math.Max(wall,p.Len()-op.Envelope)
  }}
 }
 v:=&VolumeGrid{Space:d,Values:values}
 applyConstraints(v)
 return v,nil
}
