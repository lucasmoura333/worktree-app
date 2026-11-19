import React from "react";
import {
  AbsoluteFill,
  Easing,
  Img,
  Sequence,
  interpolate,
  spring,
  staticFile,
  useCurrentFrame,
  useVideoConfig,
} from "remotion";

// Paleta do tema Street-Art do próprio app.
const INK = "#20211f";
const PAPER = "#efebe1";
const CARD = "#fffdf6";
const LIME = "#d8ef5b";
const ORANGE = "#ff5a2c";
const BLUE = "#2548dc";
const FONT = "'Helvetica Neue', Helvetica, Arial, sans-serif";
const MONO = "'DejaVu Sans Mono', Menlo, monospace";

type Cam = { s: number; x: number; y: number };

// Imagem 2x (2560x1440) em quadro 1280x720 com zoom lento até um ponto.
const Shot: React.FC<{ src: string; from: Cam; to: Cam; dur: number }> = ({ src, from, to, dur }) => {
  const f = useCurrentFrame();
  const t = interpolate(f, [0, dur], [0, 1], { extrapolateRight: "clamp", easing: Easing.inOut(Easing.cubic) });
  const s = from.s + (to.s - from.s) * t;
  const x = from.x + (to.x - from.x) * t;
  const y = from.y + (to.y - from.y) * t;
  return (
    <AbsoluteFill style={{ overflow: "hidden", background: PAPER }}>
      <Img
        src={staticFile(src)}
        style={{ width: 1280, height: 720, transformOrigin: `${x}px ${y}px`, transform: `scale(${s})` }}
      />
    </AbsoluteFill>
  );
};

const Fade: React.FC<{ dur: number; children: React.ReactNode }> = ({ dur, children }) => {
  const f = useCurrentFrame();
  const o = interpolate(f, [0, 8, dur - 8, dur], [0, 1, 1, 0], { extrapolateLeft: "clamp", extrapolateRight: "clamp" });
  return <AbsoluteFill style={{ opacity: o }}>{children}</AbsoluteFill>;
};

const Caption: React.FC<{ step: string; title: string; sub?: string }> = ({ step, title, sub }) => {
  const f = useCurrentFrame();
  const { fps } = useVideoConfig();
  const k = spring({ frame: f - 6, fps, config: { damping: 14, stiffness: 120 } });
  return (
    <div
      style={{
        position: "absolute",
        left: 40,
        bottom: 36,
        maxWidth: 760,
        transform: `translateY(${(1 - k) * 60}px) rotate(${(1 - k) * -2}deg)`,
        opacity: k,
        background: CARD,
        border: `3px solid ${INK}`,
        boxShadow: `8px 8px 0 ${INK}`,
        padding: "16px 22px 18px",
        fontFamily: FONT,
        color: INK,
      }}
    >
      <div
        style={{
          position: "absolute",
          top: -18,
          left: 16,
          background: LIME,
          border: `2px solid ${INK}`,
          padding: "2px 10px",
          fontFamily: MONO,
          fontSize: 15,
          fontWeight: 700,
          transform: "rotate(-2deg)",
        }}
      >
        {step}
      </div>
      <div style={{ fontSize: 34, fontWeight: 800, letterSpacing: -0.5, lineHeight: 1.1 }}>{title}</div>
      {sub ? <div style={{ fontSize: 19, marginTop: 6, lineHeight: 1.35 }}>{sub}</div> : null}
    </div>
  );
};

const Scene: React.FC<{
  dur: number;
  shots: { src: string; from: Cam; to: Cam }[];
  step: string;
  title: string;
  sub?: string;
}> = ({ dur, shots, step, title, sub }) => {
  const per = Math.floor(dur / shots.length);
  return (
    <Fade dur={dur}>
      {shots.map((s, i) => (
        <Sequence key={s.src} from={i * per} durationInFrames={i === shots.length - 1 ? dur - i * per : per + 10}>
          <CrossIn first={i === 0}>
            <Shot {...s} dur={per} />
          </CrossIn>
        </Sequence>
      ))}
      <Caption step={step} title={title} sub={sub} />
    </Fade>
  );
};

const CrossIn: React.FC<{ first: boolean; children: React.ReactNode }> = ({ first, children }) => {
  const f = useCurrentFrame();
  const o = first ? 1 : interpolate(f, [0, 10], [0, 1], { extrapolateRight: "clamp" });
  return <AbsoluteFill style={{ opacity: o }}>{children}</AbsoluteFill>;
};

const Intro: React.FC<{ dur: number }> = ({ dur }) => {
  const f = useCurrentFrame();
  const { fps } = useVideoConfig();
  const a = spring({ frame: f, fps, config: { damping: 13 } });
  const b = spring({ frame: f - 10, fps, config: { damping: 13 } });
  const c = spring({ frame: f - 22, fps, config: { damping: 15 } });
  return (
    <Fade dur={dur}>
      <AbsoluteFill style={{ background: PAPER, fontFamily: FONT, color: INK }}>
        <div
          style={{
            position: "absolute",
            right: -60,
            top: 70,
            width: 700,
            transform: `translateX(${(1 - c) * 300}px) rotate(${-4 + (1 - c) * 6}deg)`,
            border: `3px solid ${INK}`,
            boxShadow: `12px 12px 0 ${INK}`,
            opacity: c,
          }}
        >
          <Img src={staticFile("cards-street.png")} style={{ width: "100%", display: "block" }} />
        </div>
        <div style={{ position: "absolute", left: 70, top: 190, transform: `translateY(${(1 - a) * 40}px)`, opacity: a }}>
          <div
            style={{
              display: "inline-block",
              background: LIME,
              border: `2px solid ${INK}`,
              padding: "4px 12px",
              fontFamily: MONO,
              fontSize: 18,
              fontWeight: 700,
              transform: "rotate(-2deg)",
            }}
          >
            git worktrees · Obsidian · Go
          </div>
          <div style={{ fontSize: 96, fontWeight: 900, letterSpacing: -3, lineHeight: 0.95, marginTop: 22 }}>
            worktree
            <span style={{ color: ORANGE }}>-</span>
            app
          </div>
        </div>
        <div
          style={{
            position: "absolute",
            left: 74,
            top: 410,
            width: 520,
            fontSize: 28,
            lineHeight: 1.3,
            transform: `translateY(${(1 - b) * 40}px)`,
            opacity: b,
          }}
        >
          Várias tarefas em paralelo, <b>uma tela</b> para criar, acompanhar e remover worktrees sem medo.
        </div>
      </AbsoluteFill>
    </Fade>
  );
};

const THEMES = [
  ["cards-street.png", "Street-Art"],
  ["cards-neo.png", "Neo Brutalismo"],
  ["cards-grunge.png", "Grunge"],
  ["cards-frasurbane.png", "Frasurbane"],
] as const;

const Themes: React.FC<{ dur: number }> = ({ dur }) => {
  const f = useCurrentFrame();
  const { fps } = useVideoConfig();
  return (
    <Fade dur={dur}>
      <AbsoluteFill style={{ background: PAPER, fontFamily: FONT, color: INK }}>
        <div style={{ position: "absolute", left: 60, top: 34, fontSize: 40, fontWeight: 900, letterSpacing: -1 }}>
          Quatro modelos visuais
        </div>
        {THEMES.map(([src, name], i) => {
          const k = spring({ frame: f - 6 - i * 7, fps, config: { damping: 14 } });
          const col = i % 2;
          const row = Math.floor(i / 2);
          return (
            <div
              key={src}
              style={{
                position: "absolute",
                left: 60 + col * 590,
                top: 110 + row * 300,
                width: 560,
                border: `3px solid ${INK}`,
                boxShadow: `7px 7px 0 ${INK}`,
                background: CARD,
                transform: `scale(${0.85 + 0.15 * k}) rotate(${(i % 2 ? 1 : -1) * (1 - k) * 4}deg)`,
                opacity: k,
              }}
            >
              <Img src={staticFile(src)} style={{ width: "100%", height: 236, objectFit: "cover", display: "block" }} />
              <div
                style={{
                  borderTop: `3px solid ${INK}`,
                  padding: "6px 12px",
                  fontFamily: MONO,
                  fontSize: 16,
                  fontWeight: 700,
                  background: i === 0 ? LIME : CARD,
                }}
              >
                {name}
              </div>
            </div>
          );
        })}
      </AbsoluteFill>
    </Fade>
  );
};

const Outro: React.FC<{ dur: number }> = ({ dur }) => {
  const f = useCurrentFrame();
  const { fps } = useVideoConfig();
  const k = spring({ frame: f, fps, config: { damping: 14 } });
  const lines = [
    ["examples/vault", "Vault de exemplo com kanbans, savepoints e anexos"],
    ["127.0.0.1", "só local; nada de push, só git fetch"],
    ["MIT", "github.com/lucasmoura333/worktree-app"],
  ];
  return (
    <Fade dur={dur + 8}>
      <AbsoluteFill style={{ background: INK, color: PAPER, fontFamily: FONT, padding: 80 }}>
        <div style={{ fontSize: 80, fontWeight: 900, letterSpacing: -2.5, transform: `translateY(${(1 - k) * 30}px)`, opacity: k }}>
          Branch out.
          <br />
          <span style={{ color: LIME }}>Stay connected.</span>
        </div>
        <div style={{ marginTop: 44 }}>
          {lines.map(([tag, text], i) => {
            const kk = spring({ frame: f - 12 - i * 6, fps, config: { damping: 15 } });
            return (
              <div key={tag} style={{ display: "flex", alignItems: "center", gap: 18, marginTop: 14, opacity: kk, transform: `translateX(${(1 - kk) * -30}px)` }}>
                <span
                  style={{
                    background: i === 2 ? ORANGE : i === 1 ? BLUE : LIME,
                    color: i === 0 ? INK : "#fff",
                    fontFamily: MONO,
                    fontWeight: 700,
                    fontSize: 18,
                    padding: "3px 10px",
                    minWidth: 150,
                    textAlign: "center",
                  }}
                >
                  {tag}
                </span>
                <span style={{ fontSize: 26 }}>{text}</span>
              </div>
            );
          })}
        </div>
      </AbsoluteFill>
    </Fade>
  );
};

// Linha do tempo (frames a 30 fps).
const S = (s: number, x: number, y: number): Cam => ({ s, x, y });
type Part = { dur: number; el: (dur: number) => React.ReactNode };
const PARTS: Part[] = [
  { dur: 90, el: (d) => <Intro dur={d} /> },
  {
    dur: 150,
    el: (d) => (
      <Scene
        dur={d}
        step="01 · mural"
        title="Um card por worktree"
        sub="Agrupado por frente: alterações, upstream, .env faltando e o último savepoint."
        shots={[
          { src: "cards-street.png", from: S(1, 640, 360), to: S(1.45, 420, 330) },
          { src: "cards-street-2.png", from: S(1.15, 420, 330), to: S(1.5, 430, 300) },
        ]}
      />
    ),
  },
  {
    dur: 120,
    el: (d) => (
      <Scene
        dur={d}
        step="02 · criar"
        title="Uma feature, vários repos"
        sub="Worktree, .env copiado, porta do Docker e prévia antes de criar."
        shots={[{ src: "create.png", from: S(1, 640, 300), to: S(1.3, 640, 330) }]}
      />
    ),
  },
  {
    dur: 120,
    el: (d) => (
      <Scene
        dur={d}
        step="03 · savepoints"
        title="O contexto vem junto"
        sub="O frontmatter do savepoint liga a worktree à frente, sem copiar nada à mão."
        shots={[{ src: "reader-savepoint.png", from: S(1, 640, 360), to: S(1.35, 330, 380) }]}
      />
    ),
  },
  {
    dur: 150,
    el: (d) => (
      <Scene
        dur={d}
        step="04 · leitor"
        title="Notas com imagens e anexos"
        sub="SVG, PDF, CSV e link quebrado marcado, tudo dentro do escopo do Vault."
        shots={[
          { src: "reader-fluxo.png", from: S(1, 640, 360), to: S(1.3, 640, 280) },
          { src: "reader-fluxo-anexos.png", from: S(1.1, 640, 500), to: S(1.35, 640, 560) },
        ]}
      />
    ),
  },
  {
    dur: 150,
    el: (d) => (
      <Scene
        dur={d}
        step="05 · mapa"
        title="Grafo local das notas"
        sub="Frente → worktree → savepoint, no estilo do Obsidian."
        shots={[
          { src: "map.png", from: S(1, 640, 360), to: S(1.35, 760, 300) },
          { src: "map-kanban.png", from: S(1.15, 1000, 330), to: S(1.4, 1000, 300) },
        ]}
      />
    ),
  },
  { dur: 120, el: (d) => <Themes dur={d} /> },
  { dur: 120, el: (d) => <Outro dur={d} /> },
];

export const TOTAL = PARTS.reduce((n, p) => n + p.dur, 0);

export const Demo: React.FC = () => {
  let at = 0;
  return (
    <AbsoluteFill style={{ background: PAPER }}>
      {PARTS.map((p, i) => {
        const from = at;
        at += p.dur;
        return (
          <Sequence key={i} from={from} durationInFrames={p.dur}>
            {p.el(p.dur)}
          </Sequence>
        );
      })}
    </AbsoluteFill>
  );
};
