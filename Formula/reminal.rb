class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.10.3"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.3/reminal_0.10.3_darwin_arm64.tar.gz"
      sha256 "0f6324593daa3ed660e8f5626f4bcf1494244cb0bd33d5fdd4b76830205ad432"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.3/reminal_0.10.3_darwin_amd64.tar.gz"
      sha256 "a4d6ff22b9af62bf3745e8305ab50d512fb998d20099914e6053db5c7fd17d99"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.3/reminal_0.10.3_linux_arm64.tar.gz"
      sha256 "feb2f0c80a88d5c16c6f7ce179055b35c8a722d29a0fcd3d97f27e9bb927e982"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.3/reminal_0.10.3_linux_amd64.tar.gz"
      sha256 "d33bf4d368be2542b4ee5aa880cc02602b19889ca1d2fb2f95a5e3910cf4ded9"
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
