import qrcode from "qrcode-generator";
import { useMemo } from "react";

/**
 * QrCode desenha o código como SVG, montado aqui a partir da matriz.
 *
 * A biblioteca só calcula a matriz (é o que não vale escrever à mão: a
 * correção de erros e as máscaras). O desenho fica conosco para não
 * injetar HTML pronto na página.
 *
 * Preto no branco mesmo no tema escuro: leitor de QR code espera módulo
 * escuro em fundo claro, e o invertido falha em muito aplicativo. A borda
 * branca de quatro módulos é a zona de silêncio que a especificação pede.
 */
export function QrCode({ value, label, size = 184 }: { value: string; label: string; size?: number }) {
  const { path, total } = useMemo(() => {
    const qr = qrcode(0, "M");
    qr.addData(value);
    qr.make();

    const n = qr.getModuleCount();
    const margem = 4;
    let d = "";
    for (let linha = 0; linha < n; linha++) {
      for (let coluna = 0; coluna < n; coluna++) {
        if (qr.isDark(linha, coluna)) d += `M${coluna + margem} ${linha + margem}h1v1h-1z`;
      }
    }
    return { path: d, total: n + margem * 2 };
  }, [value]);

  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${total} ${total}`}
      width={size}
      height={size}
      shapeRendering="crispEdges"
      className="rounded-sm"
    >
      <rect width={total} height={total} fill="#fff" />
      <path d={path} fill="#000" />
    </svg>
  );
}
