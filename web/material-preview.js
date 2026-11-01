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
float previewNoise(vec3 p) {
  vec3 i = floor(p), f = fract(p); f = f*f*(3.0-2.0*f);
  return mix(mix(mix(previewHash(i),previewHash(i+vec3(1,0,0)),f.x),
                 mix(previewHash(i+vec3(0,1,0)),previewHash(i+vec3(1,1,0)),f.x),f.y),
             mix(mix(previewHash(i+vec3(0,0,1)),previewHash(i+vec3(1,0,1)),f.x),
                 mix(previewHash(i+vec3(0,1,1)),previewHash(i+vec3(1,1,1)),f.x),f.y),f.z);
}`;

export function previewMaterial(preset, domainSize = 1, wireframe = false) {
  if (wireframe) return new THREE.MeshBasicMaterial({ color: 0xffc477, wireframe: true, transparent: true, opacity: .38, depthWrite: false });
  const material = new THREE.MeshStandardMaterial({ ...MATERIAL_PRESETS[preset], side: THREE.DoubleSide });
  if (preset !== "stone" && preset !== "wood") return material;
  // Canonical Y-up scene coordinates: GLTF node transforms and artifact meshes
  // share the same frame. This avoids UV seams and keeps reference/result grain
  // aligned; changing resolution never changes the texture's spatial scale.
  material.onBeforeCompile = shader => {
    shader.uniforms.previewScale = { value: 6 / Math.max(domainSize, .001) };
    shader.vertexShader = "varying vec3 previewPosition;\n" + shader.vertexShader;
    shader.vertexShader = shader.vertexShader.replace("#include <begin_vertex>", "#include <begin_vertex>\npreviewPosition = (modelMatrix * vec4(position, 1.0)).xyz;");
    shader.fragmentShader = "varying vec3 previewPosition;\nuniform float previewScale;\n" + noise + "\n" + shader.fragmentShader;
    const pattern = preset === "wood" ? `
      vec3 p = previewPosition * previewScale;
      float warp = previewNoise(p * vec3(.35,2.0,2.0));
      float rings = length(p.yz + vec2(warp*.18)) * 18.0;
      // Filter fine grain as it becomes smaller than a pixel.
      float band = .5 + .5*sin(rings) * (1.0-smoothstep(.3,2.0,fwidth(rings)));
      float grain = previewNoise(p * vec3(2.0,30.0,30.0));
      diffuseColor.rgb *= mix(vec3(.16,.055,.017),vec3(.59,.30,.10),.25+.65*band) * (.85+.15*grain);
    ` : `
      vec3 p = previewPosition * previewScale;
      float mottle = .65*previewNoise(p*3.0)+.35*previewNoise(p*13.0);
      diffuseColor.rgb *= .65 + .55*mottle;
    `;
    shader.fragmentShader = shader.fragmentShader.replace("#include <color_fragment>", "#include <color_fragment>\n" + pattern);
  };
  material.customProgramCacheKey = () => `progen-preview-v1-${preset}`;
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
