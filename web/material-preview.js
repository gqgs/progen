import * as THREE from "three";
import { RoomEnvironment } from "three/addons/environments/RoomEnvironment.js";

// Versioned, deterministic display presets. No recipe or export state lives here.
export const MATERIAL_PRESETS = {
  clay: { color: 0xb97959, roughness: .88, metalness: 0 },
  stone: { color: 0xaaa79e, roughness: .94, metalness: 0 },
  wood: { color: 0xffffff, roughness: .62, metalness: 0 },
  metal: { color: 0xc1c8d0, roughness: .23, metalness: 1 },
  glass: { color: 0xb4dce4, roughness: .12, metalness: 0, transparent: true, opacity: .28, depthWrite: false },
};

const noise = `
float previewHash(vec3 p) {
  return fract(sin(dot(p, vec3(127.1,311.7,74.7))) * 43758.5453);
}
// Value and analytic gradient of smooth value noise. Analytic derivatives avoid
// differentiating pixel-filter weights or evaluating shifted noise samples.
vec4 previewNoiseGradient(vec3 p) {
  vec3 i = floor(p), f = fract(p);
  vec3 u = f*f*(3.0-2.0*f), du = 6.0*f*(1.0-f);
  float a = previewHash(i), b = previewHash(i+vec3(1,0,0));
  float c = previewHash(i+vec3(0,1,0)), d = previewHash(i+vec3(1,1,0));
  float e = previewHash(i+vec3(0,0,1)), f1 = previewHash(i+vec3(1,0,1));
  float g = previewHash(i+vec3(0,1,1)), h = previewHash(i+vec3(1,1,1));
  float ab = mix(a,b,u.x), cd = mix(c,d,u.x);
  float ef = mix(e,f1,u.x), gh = mix(g,h,u.x);
  float value = mix(mix(ab,cd,u.y),mix(ef,gh,u.y),u.z);
  vec3 gradient = du * vec3(
    mix(mix(b-a,d-c,u.y),mix(f1-e,h-g,u.y),u.z),
    mix(cd-ab,gh-ef,u.z), mix(ef,gh,u.y)-mix(ab,cd,u.y));
  return vec4(value,gradient);
}
float previewNoise(vec3 p) { return previewNoiseGradient(p).x; }
float previewDetailFilter(vec3 p) {
  // Remove an octave smoothly before its cells become smaller than a pixel.
  float footprint = max(length(dFdx(p)),length(dFdy(p)));
  return 1.0-smoothstep(.3,1.0,footprint);
}
`;

export function previewMaterial(preset, domainSize = 1, wireframe = false) {
  if (wireframe) return new THREE.MeshBasicMaterial({ color: 0xffc477, wireframe: true, transparent: true, opacity: .38, depthWrite: false });
  const material = new THREE.MeshStandardMaterial({ ...MATERIAL_PRESETS[preset], side: THREE.DoubleSide });
  if (!["clay", "stone", "wood"].includes(preset)) return material;
  // Canonical Y-up scene coordinates keep node-transformed GLB meshes and
  // reference/result artifacts aligned at every generation resolution.
  material.onBeforeCompile = shader => {
    shader.uniforms.previewScale = { value: 6 / Math.max(domainSize, .001) };
    shader.vertexShader = "varying vec3 previewPosition;\n" + shader.vertexShader;
    shader.vertexShader = shader.vertexShader.replace("#include <begin_vertex>", "#include <begin_vertex>\npreviewPosition = (modelMatrix * vec4(position, 1.0)).xyz;");
    shader.fragmentShader = "varying vec3 previewPosition;\nuniform float previewScale;\n" + noise + "\n" + shader.fragmentShader;
    const pattern = preset === "wood" ? `
      vec3 previewP = previewPosition * previewScale;
      vec3 warpAxes = vec3(.35,2.0,2.0);
      vec4 warp = previewNoiseGradient(previewP * warpAxes);
      vec2 ringPosition = previewP.yz + vec2(warp.x*.18);
      float radius = max(length(ringPosition),.0001);
      float rings = radius * 18.0;
      float ringFilter = 1.0-smoothstep(.3,2.0,fwidth(rings));
      float band = .5 + .5*sin(rings)*ringFilter;
      vec3 grainAxes = vec3(2.0,30.0,30.0);
      vec4 grainNoise = previewNoiseGradient(previewP * grainAxes);
      float grainFilter = previewDetailFilter(previewP * grainAxes);
      float grain = mix(.5,grainNoise.x,grainFilter);
      diffuseColor.rgb *= mix(vec3(.16,.055,.017),vec3(.59,.30,.10),.25+.65*band) * (.85+.15*grain);
      vec3 ringGradient = 18.0 * (vec3(0.0,ringPosition/radius)
        + .18*(ringPosition.x+ringPosition.y)/radius * warp.yzw*warpAxes);
      // Shallow pores run with the grain; growth rings add broader relief.
      vec3 previewGradient = .004*cos(rings)*ringGradient*ringFilter
        + .003*grainNoise.yzw*grainAxes*grainFilter;
      float previewRoughnessDelta = .10*(grain-.5)+.045*(band-.5);
    ` : preset === "stone" ? `
      vec3 previewP = previewPosition * previewScale;
      float mottle = .65*mix(.5,previewNoise(previewP*3.0),previewDetailFilter(previewP*3.0))
        + .35*mix(.5,previewNoise(previewP*13.0),previewDetailFilter(previewP*13.0));
      diffuseColor.rgb *= .65 + .55*mottle;
      vec4 aggregate = previewNoiseGradient(previewP*9.0);
      vec4 pores = previewNoiseGradient(previewP*28.0);
      float aggregateFilter = previewDetailFilter(previewP*9.0);
      float poreFilter = previewDetailFilter(previewP*28.0);
      vec3 previewGradient = .003*9.0*aggregate.yzw*aggregateFilter
        + .004*28.0*pores.yzw*poreFilter;
      float previewRoughnessDelta = .08*(aggregate.x-.5)*aggregateFilter
        + .08*(pores.x-.5)*poreFilter;
    ` : `
      vec3 previewP = previewPosition * previewScale;
      vec4 clayPores = previewNoiseGradient(previewP*36.0);
      float poreFilter = previewDetailFilter(previewP*36.0);
      vec3 previewGradient = .001*36.0*clayPores.yzw*poreFilter;
      float previewRoughnessDelta = .035*(clayPores.x-.5)*poreFilter;
    `;
    shader.fragmentShader = shader.fragmentShader.replace("#include <color_fragment>", "#include <color_fragment>\n" + pattern);
    shader.fragmentShader = shader.fragmentShader.replace("#include <roughnessmap_fragment>", `#include <roughnessmap_fragment>
      roughnessFactor = clamp(roughnessFactor + previewRoughnessDelta,.04,1.0);
    `);
    // H(worldPosition) = H(previewP) / previewScale gives a world-space height
    // gradient independent of domain size. Perturb only the lighting normal,
    // never positions. Project onto the surface tangent plane in view space.
    shader.fragmentShader = shader.fragmentShader.replace("#include <normal_fragment_maps>", `#include <normal_fragment_maps>
      vec3 previewViewGradient = mat3(viewMatrix) * previewGradient;
      previewViewGradient -= normal * dot(normal,previewViewGradient);
      normal = normalize(normal - faceDirection * previewViewGradient);
    `);
  };
  material.customProgramCacheKey = () => `progen-preview-v2-surface-${preset}`;
  return material;
}

export function applyPreview(object, preset, domainSize, wireframe = false) {
  object?.traverse(mesh => {
    if (!mesh.isMesh) return;
    const previous = Array.isArray(mesh.material) ? mesh.material : [mesh.material];
    mesh.material = previewMaterial(preset, domainSize, wireframe);
    previous.forEach(material => material?.dispose());
  });
}

export function studioEnvironment(renderer) {
  const room = new RoomEnvironment();
  const pmrem = new THREE.PMREMGenerator(renderer);
  const target = pmrem.fromScene(room, .04);
  room.dispose();
  pmrem.dispose();
  return target;
}
