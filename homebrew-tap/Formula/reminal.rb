class Reminal < Formula
  desc "Remote terminal access — secure, zero-config alternative to SSH"
  homepage "https://github.com/harshalgajjar/Reminal"
  version "0.10.5"
  license "MIT"

  head do
    url "https://github.com/harshalgajjar/Reminal.git", branch: "main"
  end

  on_macos do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.5/reminal_0.10.5_darwin_arm64.tar.gz"
      sha256 "c1e0e4cb0e1a67e58b977a8a1a68ab7f52a43736e70468481d69c6f2c1893976"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.5/reminal_0.10.5_darwin_amd64.tar.gz"
      sha256 "722e2361233d4fc024488ad3d6347ff00825b2247bf0473f7d5d34df68112758"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.5/reminal_0.10.5_linux_arm64.tar.gz"
      sha256 "e1e7af8b38e5a2908394668c40690bc7efa275639a44970f9b35305b38270162"
    end
    on_intel do
      url "https://github.com/harshalgajjar/Reminal/releases/download/v0.10.5/reminal_0.10.5_linux_amd64.tar.gz"
      sha256 "07bf01cae2a6249a0efcab9520bc13aa39292491ef00361c366df525079c0d5c"
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
