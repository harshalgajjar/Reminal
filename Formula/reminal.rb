class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "1.3.0"
  license "AGPL-3.0-or-later"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.3.0/reminal_1.3.0_darwin_arm64.tar.gz"
      sha256 "e158d3bd1928cfe5182f55abfb1e7129becfb532bed16bfce32d8a589148477c"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.3.0/reminal_1.3.0_darwin_amd64.tar.gz"
      sha256 "3dec3f0aca5d8887b8634737a1e3ef0042a6b8749e767285d7009d26fa3bb474"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.3.0/reminal_1.3.0_linux_arm64.tar.gz"
      sha256 "503f63f1d21b50af3f985ebfcffe4d9bc2574c8286bb5506370e5532011a9055"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v1.3.0/reminal_1.3.0_linux_amd64.tar.gz"
      sha256 "a715ca4509b6ed150316db62e38e539538c73240d4fb64c4ee9c3578848cd5ce"
    end
  end

  depends_on "go" => :build if build.head?

  def install
    if build.head?
      system "go", "build", "-ldflags=#{ldflags}", "-o", bin/"reminal", "./cmd/reminal"
    else
      bin.install "reminal"
    end
  end

  def ldflags
    "-s -w " \
      "-X main.version=#{version} " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudRelay=wss://reminal-relay.futuristic.workers.dev/ws " \
      "-X github.com/reminal/reminal/internal/config.DefaultCloudWeb=https://reminal-relay.futuristic.workers.dev"
  end

  def caveats
    <<~EOS
      reminal connects to the hosted relay automatically — no setup needed.

        reminal              # share your terminal
        reminal --connect ID --pin PIN
    EOS
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/reminal version")
  end
end
