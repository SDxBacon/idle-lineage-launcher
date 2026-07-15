/*
 * Adapted from React Bits Dock (TS-CSS variant).
 * Copyright (c) 2026 David Haz. Licensed under MIT + Commons Clause.
 * See THIRD_PARTY_NOTICES.md at the repository root.
 */
import {
  Children,
  cloneElement,
  isValidElement,
  useEffect,
  useRef,
  useState,
  type MouseEvent,
  type ReactElement,
  type ReactNode,
} from 'react';
import { motion, useMotionValue, useSpring, useTransform, type SpringOptions } from 'motion/react';
import './Dock.css';

export type DockItemData = {
  icon: ReactNode;
  label: ReactNode;
  onClick: () => void;
  className?: string;
};

type DockProps = {
  items: DockItemData[];
  distance?: number;
  baseItemSize?: number;
  magnification?: number;
  spring?: SpringOptions;
};

export function Dock({
  items,
  distance = 150,
  baseItemSize = 46,
  magnification = 66,
  spring = { mass: 0.1, stiffness: 150, damping: 12 },
}: DockProps) {
  const mouseX = useMotionValue(Number.POSITIVE_INFINITY);

  return (
    <div className="dock-overlay" aria-label="遊戲工具列">
      <motion.div
        className="dock-panel"
        onMouseMove={(event: MouseEvent) => mouseX.set(event.pageX)}
        onMouseLeave={() => mouseX.set(Number.POSITIVE_INFINITY)}
      >
        {items.map((item, index) => (
          <DockItem
            key={`${String(item.label)}-${index}`}
            mouseX={mouseX}
            spring={spring}
            distance={distance}
            baseItemSize={baseItemSize}
            magnification={magnification}
            {...item}
          />
        ))}
      </motion.div>
    </div>
  );
}

type DockItemProps = DockItemData & {
  mouseX: ReturnType<typeof useMotionValue<number>>;
  spring: SpringOptions;
  distance: number;
  baseItemSize: number;
  magnification: number;
};

function DockItem({
  icon,
  label,
  onClick,
  className = '',
  mouseX,
  spring,
  distance,
  baseItemSize,
  magnification,
}: DockItemProps) {
  const ref = useRef<HTMLButtonElement>(null);
  const hovered = useMotionValue(0);
  const mouseDistance = useTransform(mouseX, value => {
    const bounds = ref.current?.getBoundingClientRect() ?? { x: 0, width: baseItemSize };
    return value - bounds.x - bounds.width / 2;
  });
  const targetSize = useTransform(mouseDistance, [-distance, 0, distance], [baseItemSize, magnification, baseItemSize]);
  const size = useSpring(targetSize, spring);

  return (
    <motion.button
      ref={ref}
      type="button"
      className={`dock-item ${className}`}
      style={{ width: size, height: size }}
      onHoverStart={() => hovered.set(1)}
      onHoverEnd={() => hovered.set(0)}
      onFocus={() => hovered.set(1)}
      onBlur={() => hovered.set(0)}
      onClick={onClick}
      aria-label={typeof label === 'string' ? label : undefined}
    >
      <DockLabel hovered={hovered}>{label}</DockLabel>
      <span className="dock-icon" aria-hidden="true">{icon}</span>
    </motion.button>
  );
}

function DockLabel({ children, hovered }: { children: ReactNode; hovered: ReturnType<typeof useMotionValue<number>> }) {
  const [visible, setVisible] = useState(false);

  useEffect(() => hovered.on('change', value => setVisible(value === 1)), [hovered]);

  return visible ? (
    <motion.span
      className="dock-label"
      initial={{ opacity: 0, y: 4 }}
      animate={{ opacity: 1, y: 0 }}
      exit={{ opacity: 0, y: 4 }}
      role="tooltip"
    >
      {Children.map(children, child => (isValidElement(child) ? cloneElement(child as ReactElement) : child))}
    </motion.span>
  ) : null;
}
